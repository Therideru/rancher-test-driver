package driver

import (
	"context"
	"fmt"

	"github.com/hetznercloud/hcloud-go/hcloud"
	"github.com/rancher/machine/libmachine/drivers"
	"github.com/rancher/machine/libmachine/mcnflag"
	"github.com/rancher/machine/libmachine/state"
	// ... plus hcloud and other imports
)

type Driver struct {
	*drivers.BaseDriver        // Embeds fields like MachineName, StorePath, IP, SSH info
	APIToken            string // Hetzner API token for authentication
	ServerID            int    // ID of the created server (for cleanup)
	ServerType          string // e.g. "cx11" or other instance type
	Image               string // OS image name or ID (e.g. "ubuntu-20.04")
	Region              string // Datacenter location (e.g. "nbg1" for Nuremberg)
}

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
			Value:  "cx11", // default
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
		mcnflag.StringFlag{
			Name:   "hetzner-firewall",
			Usage:  "Firewall (e.g. fsn1, nbg1)",
			EnvVar: "HETZNER_Firewall",
			Value:  "firewall",
		},
	}
}

func NewDriver(machineName, storePath string) *Driver {
	return &Driver{
		BaseDriver: &drivers.BaseDriver{
			MachineName: machineName,
			StorePath:   storePath,
			SSHUser:     "root",
		},
	}
}

func (d *Driver) SetConfigFromFlags(opts drivers.DriverOptions) error {
	d.APIToken = opts.String("hetzner-api-token")
	d.ServerType = opts.String("hetzner-server-type")
	d.Image = opts.String("hetzner-image")
	d.Region = opts.String("hetzner-location")
	d.SSHUser = "root" // default user for Hetzner cloud images
	if d.APIToken == "" {
		return fmt.Errorf("hetzner-api-token is required")
	}
	return nil
}

func (d *Driver) Create() error {
	ctx := context.Background()
	// Create Hetzner Cloud API client
	client := hcloud.NewClient(hcloud.WithToken(d.APIToken))
	// Determine SSH key to use
	// (For simplicity, we will generate a new SSH key pair)
	publicKey, err := d.generateSSHKey() // custom helper to create an SSH key and set d.SSHKeyPath
	if err != nil {
		return err
	}
	// Upload the SSH public key to Hetzner Cloud (so the server will have it for root login)
	keyName := fmt.Sprintf("rancher-%s", d.MachineName)
	hkey, _, err := client.SSHKey.Create(ctx, hcloud.SSHKeyCreateOpts{
		Name:      keyName,
		PublicKey: string(publicKey),
	})
	if err != nil {
		return fmt.Errorf("creating SSH key in Hetzner Cloud: %w", err)
	}
	// Prepare server create options
	serverOpts := hcloud.ServerCreateOpts{
		Name:       d.MachineName,
		ServerType: &hcloud.ServerType{Name: d.ServerType},
		Image:      &hcloud.Image{Name: d.Image},
		Location:   &hcloud.Location{Name: d.Region},
		SSHKeys:    []*hcloud.SSHKey{hkey}, // attach the uploaded key
	}
	result, _, err := client.Server.Create(ctx, serverOpts)
	if err != nil {
		return fmt.Errorf("error creating Hetzner server: %w", err)
	}
	server := result.Server
	d.ServerID = server.ID
	// Save the server’s IPv4 address in the driver (for Rancher to connect)
	if server.PublicNet.IPv4.IP != nil {
		d.IPAddress = server.PublicNet.IPv4.IP.String()
	}
	return nil
}

func (d *Driver) Remove() error {
	ctx := context.Background()
	client := hcloud.NewClient(hcloud.WithToken(d.APIToken))

	if d.ServerID != 0 {
		// Use DeleteWithResult instead of deprecated Delete
		_, _, err := client.Server.DeleteWithResult(ctx, &hcloud.Server{ID: d.ServerID})
		if err != nil {
			return fmt.Errorf("deleting server %d: %w", d.ServerID, err)
		}
	}
	return nil
}

func (d *Driver) GetIP() (string, error) {
	return d.IPAddress, nil
}
func (d *Driver) GetSSHHostname() (string, error) {
	return d.IPAddress, nil
}
func (d *Driver) GetSSHKeyPath() string {
	return d.SSHKeyPath // path to the private key file
}
func (d *Driver) GetSSHUsername() string {
	return d.SSHUser // "root"
}
func (d *Driver) DriverName() string {
	return "hetzner" // driver name identifier
}
func (d *Driver) GetState() (state.State, error) {
	return state.Running, nil
}
func (d *Driver) GetURL() (string, error) {
	// If you want to use SSH, you could return: ssh://root@<ip>:22
	// But most Docker Machine drivers return a tcp:// URL, so we’ll do that:
	if d.IPAddress == "" {
		return "", fmt.Errorf("no IP address available yet")
	}
	return fmt.Sprintf("tcp://%s:2376", d.IPAddress), nil
}

// Start powers on the VM (no-op for Hetzner, we assume it's already running)
func (d *Driver) Start() error {
	return nil
}

// Stop powers off the VM (no-op; you could call the Hetzner API to shut down)
func (d *Driver) Stop() error {
	return nil
}

// Restart reboots the VM (no-op; you could call the Hetzner API if desired)
func (d *Driver) Restart() error {
	return nil
}

// Kill force-stops the VM (no-op; could call Hetzner’s power off API)
func (d *Driver) Kill() error {
	return nil
}
