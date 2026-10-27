package tray

// Menu text. These match the official Tailscale Windows client so users see
// the same wording; "%s" is replaced as noted. Translate here if needed.
const (
	txtLogIn            = "Log in..."
	txtWaitingApproval  = "Waiting for device approval..."
	txtConnect          = "Connect"
	txtDisconnect       = "Disconnect"
	txtThisDevice       = "This device: %s (%s)" // host name, IP
	txtThisDeviceNone   = "This device: not connected"
	txtNetworkDevices   = "Network devices"
	txtMyDevices        = "My devices"
	txtTaggedDevices    = "Tagged devices"
	txtNoDevices        = "No devices"
	txtOffline          = "%s (offline)"
	txtAddAccount       = "Add another account..."
	txtLogOut           = "Log out"
	txtExitNodes        = "Exit nodes"
	txtExitNone         = "None"
	txtRecommended      = "Recommended: %s"
	txtTailnetExitNodes = "Tailnet exit nodes"
	txtMullvad          = "Mullvad VPN"
	txtAllowLAN         = "Allow local network access"
	txtRunExitNode      = "Run exit node"
	txtPreferences      = "Preferences"
	txtUseDNS           = "Use Tailscale DNS settings"
	txtUseSubnets       = "Use Tailscale subnets"
	txtAllowIncoming    = "Allow incoming connections"
	txtRunUnattended    = "Run unattended"
	txtAutoUpdate       = "Automatically install updates"
	txtAdminConsole     = "Admin console..."
	txtUpdateAvailable  = "Update available: %s"
	txtAbout            = "About %s" // product name
	txtExit             = "Exit"

	txtServiceDown  = "%s service is not running"            // product name
	txtOtherUser    = "%s is in use by another Windows user" // product name
	txtUpdating     = "The update is being installed in the background. The connection will drop briefly."
	txtTipConnected = "%s: Connected"
	txtTipExitNode  = "%s: Connected (exit node)"
	txtTipExitDown  = "%s: Exit node offline"
	txtTipStarting  = "%s: Connecting..."
	txtTipStopped   = "%s: Disconnected"
	txtTipLoggedOut = "%s: Logged out"
	txtTipApproval  = "%s: Waiting for approval"
	txtTipDown      = "%s: Service not running"
)
