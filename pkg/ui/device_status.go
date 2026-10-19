package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pterm/pterm"
	"github.com/teknoir/cli/pkg/api"
)

func formatPercent(val float64) string {
	return fmt.Sprintf("%.2f%%", val*100)
}

// colorTunnel renders the device's tunnel label: green when usable, yellow
// while WireGuard is enabled but not active, red when off.
func colorTunnel(d *api.FleetDevice) string {
	label := d.TunnelLabel()
	switch label {
	case "wireguard", "legacy":
		return pterm.Green(label)
	case "off":
		return pterm.Red(label)
	}
	return pterm.Yellow(label)
}

func PrintDeviceStatus(device *api.FleetDevice) {
	state := device.StateOrZero()

	pterm.DefaultHeader.WithFullWidth().Println(fmt.Sprintf("Device: %s", device.Name))

	// Top level info
	pterm.DefaultSection.Println("Top Level Info")

	tunnelStr := colorTunnel(device)

	infoTable := pterm.TableData{
		{"Namespace", device.Namespace},
		{"Tunnel", tunnelStr},
		{"Config Hash", device.ConfigHash()},
		{"Last Config Change", device.LastConfigChange()},
	}
	pterm.DefaultTable.WithData(infoTable).Render()

	// Device Resources
	pterm.DefaultSection.Println("Device Resources")

	var resourceTable pterm.TableData
	resourceTable = append(resourceTable, []string{"Resource", "Usage"})
	resourceTable = append(resourceTable, []string{"CPU Usage", formatPercent(state.CPU)})
	resourceTable = append(resourceTable, []string{"Memory Usage", formatPercent(state.Memory)})
	resourceTable = append(resourceTable, []string{"Storage Usage", formatPercent(state.Storage)})
	resourceTable = append(resourceTable, []string{"Net RX Rate", fmt.Sprintf("%.2f KB/s", state.NetRx)})
	resourceTable = append(resourceTable, []string{"Net TX Rate", fmt.Sprintf("%.2f KB/s", state.NetTx)})

	pterm.DefaultTable.WithHasHeader().WithData(resourceTable).Render()

	// Device Alerts
	if len(state.Alerts) > 0 {
		pterm.DefaultSection.Println("Device Alerts")
		var alertsTable pterm.TableData
		alertsTable = append(alertsTable, []string{"Alert Name", "State", "Count"})
		for _, alert := range state.Alerts {
			stateStr := alert.State
			if strings.ToLower(stateStr) == "firing" {
				stateStr = pterm.Red(stateStr)
			} else {
				stateStr = pterm.Yellow(stateStr)
			}
			alertsTable = append(alertsTable, []string{alert.Name, stateStr, fmt.Sprintf("%d", alert.Count)})
		}
		pterm.DefaultTable.WithHasHeader().WithData(alertsTable).Render()
	}

	// Lower level: Apps
	if len(state.Apps) > 0 {
		pterm.DefaultSection.Println("Applications")
		var appsTable pterm.TableData
		appsTable = append(appsTable, []string{"App Name", "Status", "CPU", "Memory", "Ready/Total"})
		appNames := make([]string, 0, len(state.Apps))
		for name := range state.Apps {
			appNames = append(appNames, name)
		}
		sort.Strings(appNames)
		for _, appName := range appNames {
			app := state.Apps[appName]
			appStatus := pterm.Red("Stopped")
			if app.Running {
				appStatus = pterm.Green("Running")
			}
			appsTable = append(appsTable, []string{
				appName,
				appStatus,
				formatPercent(app.CPU),
				formatPercent(app.Memory),
				readyOrDash(app.Ready),
			})
		}
		pterm.DefaultTable.WithHasHeader().WithData(appsTable).Render()
	}

	fmt.Println()
}

// readyOrDash is the Ready/Total column; "-" when the device did not report it.
func readyOrDash(ready string) string {
	if ready == "" {
		return "-"
	}
	return ready
}

func PrintDevicesSummary(devices []api.FleetDevice) {
	pterm.DefaultHeader.WithFullWidth().Println("Devices Summary")

	var table pterm.TableData
	table = append(table, []string{"Device Name", "Tunnel", "CPU", "Memory", "Storage", "Alerts", "Apps", "Last Config Change"})

	for i := range devices {
		d := &devices[i]
		state := d.StateOrZero()

		tunnelStr := colorTunnel(d)

		alertsCount := state.AlertTotal()
		alertsStr := fmt.Sprintf("%d", alertsCount)
		if alertsCount > 0 {
			alertsStr = pterm.Red(alertsStr)
		} else {
			alertsStr = pterm.Green(alertsStr)
		}

		table = append(table, []string{
			d.Name,
			tunnelStr,
			formatPercent(state.CPU),
			formatPercent(state.Memory),
			formatPercent(state.Storage),
			alertsStr,
			fmt.Sprintf("%d", state.AppTotal()),
			d.LastConfigChange(),
		})
	}

	pterm.DefaultTable.WithHasHeader().WithData(table).Render()
}
