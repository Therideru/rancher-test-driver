package driver

import (
	"context"
	"fmt"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/rancher/machine/libmachine/drivers"
	"github.com/rancher/machine/libmachine/mcnflag"
	"github.com/rancher/machine/libmachine/state"
)

// Driver implements the Docker Machine interface for Hetzner Cloud.
type Driver struct {
	*drivers.BaseDriver // Embeds MachineName, StorePath, SSHUser, SSHKeyPath, IPAddress, etc.

	APIToken   string // Hetzner API token
	ServerID   int64  // ID of the created server
	SSHKeyID   int64  // ID of the uploaded SSH key
	ServerType string // e.g. "cx11"
	Image      string // e.g. "ubuntu-20.04"
	Region     string // e.g. "nbg1"
}

// NewDriver returns a fresh instance of Driver with BaseDriver initialized.
func NewDriver(machineName, storePath string) *Driver {
	return &Driver{
		BaseDriver: &drivers.BaseDriver{
			MachineName: machineName,
			StorePath:   storePath,
			SSHUser:     "root",
		},
	}
}

// GetCreateFlags defines the flags to use when provisioning a new node.
func (d *Driver) GetCreateFlags() []mcnflag.Flag {
	return []mcnflag.Flag{
		mcnflag.StringFlag{
			Name:   "hetzner-api-token",
			Usage:  "Hetzner Cloud API Token",
			EnvVar: "HETZNER_API_TOKEN",
		},
		mcnflag.StringFlag{
			Name:   "hetzner-server-type",
			Usage:  "Hetzner server type (e.g. cx11)",
			EnvVar: "HETZNER_SERVER_TYPE",
			Value:  "cx11",
		},
		mcnflag.StringFlag{
			Name:   "hetzner-image",
			Usage:  "Image name (e.g. ubuntu-20.04)",
			EnvVar: "HETZNER_IMAGE",
			Value:  "ubuntu-20.04",
		},
		mcnflag.StringFlag{
			Name:   "hetzner-location",
			Usage:  "Datacenter location (e.g. fsn1, nbg1)",
			EnvVar: "HETZNER_LOCATION",
			Value:  "nbg1",
		},
	}
}

// SetConfigFromFlags reads the flag values into the Driver's fields.
// Called automatically by Docker Machine / Rancher.
func (d *Driver) SetConfigFromFlags(opts drivers.DriverOptions) error {
	d.APIToken = opts.String("hetzner-api-token")
	d.ServerType = opts.String("hetzner-server-type")
	d.Image = opts.String("hetzner-image")
	d.Region = opts.String("hetzner-location")
	d.SSHUser = "root" // default for Hetzner Cloud images
	if d.APIToken == "" {
		return fmt.Errorf("hetzner-api-token is required")
	}
	return nil
}

// PreCreateCheck is run before Create() to validate the config.
func (d *Driver) PreCreateCheck() error {
	if d.APIToken == "" {
		return fmt.Errorf("hetzner-api-token is required")
	}
	return nil
}

// Create provisions a new Hetzner Cloud server, waits for it to be ready,
// and records its IP (and SSH key).
func (d *Driver) Create() error {
	if d.StorePath == "" {
		return fmt.Errorf("storePath is empty, cannot create SSH key")
	}

	ctx := context.Background()
	client := hcloud.NewClient(hcloud.WithToken(d.APIToken))

	// 1) Generate SSH key + write private key locally:
	publicKey, err := d.generateSSHKey()
	if err != nil {
		return fmt.Errorf("generating SSH key: %w", err)
	}

	// 2) Upload public key to Hetzner:
	keyName := fmt.Sprintf("rancher-%s", d.MachineName)
	hkey, _, err := client.SSHKey.Create(ctx, hcloud.SSHKeyCreateOpts{
		Name:      keyName,
		PublicKey: string(publicKey),
	})
	if err != nil {
		return fmt.Errorf("creating SSH key in Hetzner Cloud: %w", err)
	}
	d.SSHKeyID = hkey.ID

	// 3) Create server with that SSH key attached:
	serverOpts := hcloud.ServerCreateOpts{
		Name:       d.MachineName,
		ServerType: &hcloud.ServerType{Name: d.ServerType},
		Image:      &hcloud.Image{Name: d.Image},
		Location:   &hcloud.Location{Name: d.Region},
		SSHKeys:    []*hcloud.SSHKey{hkey},
	}
	createResult, _, err := client.Server.Create(ctx, serverOpts)
	if err != nil {
		return fmt.Errorf("error creating Hetzner server: %w", err)
	}

	server := createResult.Server
	d.ServerID = server.ID

	// 4) Wait for the “create” action to complete:
	if createResult.Action != nil {
		if err := client.Action.WaitForFunc(ctx,
			func(a *hcloud.Action) error { return nil },
			createResult.Action,
		); err != nil {
			return fmt.Errorf("waiting for server creation: %w", err)
		}
	}
	// 5) Poll until the server has a public IPv4:
	var srv *hcloud.Server
	for i := 0; i < 30; i++ {
		srv, _, err = client.Server.GetByID(ctx, d.ServerID)
		if err != nil {
			return fmt.Errorf("fetching server %d: %w", d.ServerID, err)
		}
		if srv.PublicNet.IPv4.IP != nil {
			d.IPAddress = srv.PublicNet.IPv4.IP.String()
			break
		}
		time.Sleep(2 * time.Second)
	}
	if d.IPAddress == "" {
		return fmt.Errorf("server %d has no public IPv4 after timeout", d.ServerID)
	}

	return nil
}

// Remove deletes the Hetzner server and the uploaded SSH key.
func (d *Driver) Remove() error {
	ctx := context.Background()
	client := hcloud.NewClient(hcloud.WithToken(d.APIToken))

	// 1) Delete the server
	if d.ServerID != 0 {
		delRes, _, err := client.Server.DeleteWithResult(ctx, &hcloud.Server{ID: d.ServerID})
		if err != nil {
			return fmt.Errorf("deleting server %d: %w", d.ServerID, err)
		}
		if delRes.Action != nil {
			if err := client.Action.WaitForFunc(ctx,
				func(a *hcloud.Action) error { return nil },
				delRes.Action,
			); err != nil {
				return fmt.Errorf("waiting for server deletion: %w", err)
			}
		}
	}

	// 2) Delete the SSH key
	if d.SSHKeyID != 0 {
		if _, err := client.SSHKey.Delete(ctx, &hcloud.SSHKey{ID: d.SSHKeyID}); err != nil {
			return fmt.Errorf("deleting SSH key %d: %w", d.SSHKeyID, err)
		}
	}

	return nil
}

// GetIP returns the node's IP address
func (d *Driver) GetIP() (string, error) {
	if d.IPAddress == "" {
		return "", fmt.Errorf("IP address not available")
	}
	return d.IPAddress, nil
}

// GetSSHHostname returns the hostname used for SSH (same as IP here)
func (d *Driver) GetSSHHostname() (string, error) {
	return d.GetIP()
}

// GetSSHKeyPath returns the path to the private SSH key file
func (d *Driver) GetSSHKeyPath() string {
	return d.SSHKeyPath
}

// GetSSHUsername returns the SSH user (root)
func (d *Driver) GetSSHUsername() string {
	return d.SSHUser
}

// DriverName returns the identifier name that Rancher uses for this driver
func (d *Driver) DriverName() string {
	return "hetzner"
}

// GetState queries Hetzner for the server status (Running, Stopped, etc.)
func (d *Driver) GetState() (state.State, error) {
	if d.ServerID == 0 {
		return state.Error, fmt.Errorf("server ID not set")
	}
	ctx := context.Background()
	client := hcloud.NewClient(hcloud.WithToken(d.APIToken))
	srv, _, err := client.Server.GetByID(ctx, d.ServerID)
	if err != nil {
		return state.Error, fmt.Errorf("fetching server %d: %w", d.ServerID, err)
	}
	switch srv.Status {
	case hcloud.ServerStatusRunning:
		return state.Running, nil
	case hcloud.ServerStatusOff:
		return state.Stopped, nil
	default:
		return state.None, nil
	}
}

// GetURL returns the Docker endpoint URL (tcp://<IP>:2376)
func (d *Driver) GetURL() (string, error) {
	if d.IPAddress == "" {
		return "", fmt.Errorf("no IP address available yet")
	}
	return fmt.Sprintf("tcp://%s:2376", d.IPAddress), nil
}

// Start powers on the VM (uses Hetzner Cloud PowerOn API)
func (d *Driver) Start() error {
	if d.ServerID == 0 {
		return fmt.Errorf("server ID not set")
	}
	ctx := context.Background()
	client := hcloud.NewClient(hcloud.WithToken(d.APIToken))
	srv, _, err := client.Server.GetByID(ctx, d.ServerID)
	if err != nil {
		return fmt.Errorf("cannot fetch server %d: %w", d.ServerID, err)
	}
	if srv != nil {
		if _, _, err := client.Server.Poweron(ctx, srv); err != nil {
			return fmt.Errorf("powering on server %d: %w", d.ServerID, err)
		}
	}
	return nil
}

// Stop powers off the VM (uses Hetzner Cloud PowerOff API)
func (d *Driver) Stop() error {
	if d.ServerID == 0 {
		return fmt.Errorf("server ID not set")
	}
	ctx := context.Background()
	client := hcloud.NewClient(hcloud.WithToken(d.APIToken))
	srv, _, err := client.Server.GetByID(ctx, d.ServerID)
	if err != nil {
		return fmt.Errorf("cannot fetch server %d: %w", d.ServerID, err)
	}
	if srv != nil {
		if _, _, err := client.Server.Poweroff(ctx, srv); err != nil {
			return fmt.Errorf("powering off server %d: %w", d.ServerID, err)
		}
	}
	return nil
}

// Restart reboots the VM
func (d *Driver) Restart() error {
	if d.ServerID == 0 {
		return fmt.Errorf("server ID not set")
	}
	ctx := context.Background()
	client := hcloud.NewClient(hcloud.WithToken(d.APIToken))
	srv, _, err := client.Server.GetByID(ctx, d.ServerID)
	if err != nil {
		return fmt.Errorf("cannot fetch server %d: %w", d.ServerID, err)
	}
	if srv != nil {
		if _, _, err := client.Server.Reboot(ctx, srv); err != nil {
			return fmt.Errorf("rebooting server %d: %w", d.ServerID, err)
		}
	}
	return nil
}

// Kill forcibly powers off the VM (alias for PowerOff)
func (d *Driver) Kill() error {
	return d.Stop()
}
