package ui

import (
	"fmt"
	"strings"

	"github.com/pterm/pterm"
	"github.com/teknoir/cli/pkg/api"
)

func formatPercent(val float64) string {
	return fmt.Sprintf("%.2f%%", val*100)
}

// colorTunnel renders the device's tunnel label: green when usable, yellow
// while WireGuard is enabled but not active, red when off.
func colorTunnel(d *api.BackstageDevice) string {
	label := d.TunnelLabel()
	switch label {
	case "wireguard", "legacy":
		return pterm.Green(label)
	case "off":
		return pterm.Red(label)
	}
	return pterm.Yellow(label)
}

func PrintDeviceStatus(device *api.BackstageDevice) {
	status := device.Spec.Subresources.Status
	state := status.State

	pterm.DefaultHeader.WithFullWidth().Println(fmt.Sprintf("Device: %s", device.Metadata.Name))

	// Top level info
	pterm.DefaultSection.Println("Top Level Info")

	tunnelStr := colorTunnel(device)

	infoTable := pterm.TableData{
		{"Namespace", device.Metadata.Namespace},
		{"Tunnel", tunnelStr},
		{"Config Hash", status.Config.ActiveConfigHash},
		{"Last Config Change", status.Config.LastConfigChange},
	}
	pterm.DefaultTable.WithData(infoTable).Render()

	// Device Resources
	pterm.DefaultSection.Println("Device Resources")

	var resourceTable pterm.TableData
	resourceTable = append(resourceTable, []string{"Resource", "Usage"})
	resourceTable = append(resourceTable, []string{"CPU Usage", formatPercent(state.CpuUsage)})
	resourceTable = append(resourceTable, []string{"Memory Usage", formatPercent(state.MemoryUsage)})
	resourceTable = append(resourceTable, []string{"Storage Usage", formatPercent(state.StorageUsage)})
	resourceTable = append(resourceTable, []string{"Net RX Rate", fmt.Sprintf("%.2f KB/s", state.NetRxRate)})
	resourceTable = append(resourceTable, []string{"Net TX Rate", fmt.Sprintf("%.2f KB/s", state.NetTxRate)})

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
		for appName, app := range state.Apps {
			appStatus := pterm.Red("Stopped")
			if app.Running {
				appStatus = pterm.Green("Running")
			}
			appsTable = append(appsTable, []string{
				appName,
				appStatus,
				formatPercent(app.CpuUsagePct),
				formatPercent(app.MemoryUsagePct),
				fmt.Sprintf("%d/%d", app.ReadyContainers, app.TotalContainers),
			})
		}
		pterm.DefaultTable.WithHasHeader().WithData(appsTable).Render()
	}

	fmt.Println()
}

func PrintDevicesSummary(devices []api.BackstageDevice) {
	pterm.DefaultHeader.WithFullWidth().Println("Devices Summary")

	var table pterm.TableData
	table = append(table, []string{"Device Name", "Tunnel", "CPU", "Memory", "Storage", "Alerts", "Apps", "Last Config Change"})

	for _, d := range devices {
		status := d.Spec.Subresources.Status
		state := status.State

		tunnelStr := colorTunnel(&d)

		alertsCount := 0
		for _, a := range state.Alerts {
			alertsCount += a.Count
		}
		alertsStr := fmt.Sprintf("%d", alertsCount)
		if alertsCount > 0 {
			alertsStr = pterm.Red(alertsStr)
		} else {
			alertsStr = pterm.Green(alertsStr)
		}

		appsCount := len(state.Apps)

		table = append(table, []string{
			d.Metadata.Name,
			tunnelStr,
			formatPercent(state.CpuUsage),
			formatPercent(state.MemoryUsage),
			formatPercent(state.StorageUsage),
			alertsStr,
			fmt.Sprintf("%d", appsCount),
			status.Config.LastConfigChange,
		})
	}

	pterm.DefaultTable.WithHasHeader().WithData(table).Render()
}
