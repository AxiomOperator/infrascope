import { alertInfo, monitorAlertInfo } from "@/lib/alerts"
import { getMonitorName } from "@/lib/network-monitor-utils"
import { $alerts, $allSystemsById } from "@/lib/stores"
import { useDownMonitors } from "@/lib/use-network-monitors"
import type { AlertRecord, NetworkMonitorRecord } from "@/types"
import { Plural, Trans } from "@lingui/react/macro"
import { useStore } from "@nanostores/react"
import { getPagePath } from "@nanostores/router"
import { useMemo, useState } from "react"
import { AlertBannerSheet, AlertBannerSheetItem } from "./alert-banner-sheet"
import { $router } from "./router"

function AlertTriggeredDesc({ alert }: { alert: AlertRecord }) {
	const info = alertInfo[alert.name as keyof typeof alertInfo]
	if (info.triggeredDesc) {
		return info.triggeredDesc()
	}
	if (alert.name === "NetworkMonitorLoss") {
		return <Trans>One or more monitors exceed {alert.value}% loss</Trans>
	}
	if (alert.name === "Status") {
		return <Trans>Connection is down</Trans>
	}
	if (info.invert) {
		return (
			<Trans>
				Below {alert.value}
				{info.unit} in last <Plural value={alert.min} one="# minute" other="# minutes" />
			</Trans>
		)
	}
	return (
		<Trans>
			Exceeds {alert.value}
			{info.unit} in last <Plural value={alert.min} one="# minute" other="# minutes" />
		</Trans>
	)
}

function AlertLabel({ alert, systemName }: { alert: AlertRecord; systemName?: string }) {
	const info = alertInfo[alert.name as keyof typeof alertInfo]
	return (
		<>
			{systemName} <span className="opacity-60 font-normal">·</span> {info.name()}
		</>
	)
}

function MonitorDownLabel({ monitor }: { monitor: NetworkMonitorRecord }) {
	return (
		<>
			{getMonitorName(monitor)} <span className="opacity-60 font-normal">·</span> {monitorAlertInfo.MonitorDown.name()}
		</>
	)
}

function MonitorDownDesc({ monitor }: { monitor: NetworkMonitorRecord }) {
	return monitor.lastError || monitorAlertInfo.MonitorDown.triggeredDesc?.()
}

/** Banner showing the number of triggered alerts and down monitors, with a sheet listing them. */
export const ActiveAlerts = ({ className }: { className?: string }) => {
	const alerts = useStore($alerts)
	const systems = useStore($allSystemsById)
	const [open, setOpen] = useState(false)
	const downMonitors = useDownMonitors()
	const downMonitorsKey = downMonitors.map((m) => `${m.id}${m.name}${m.lastError}`).join("")

	const { activeAlerts, systemCount, alertsKey } = useMemo(() => {
		const activeAlerts: AlertRecord[] = []
		const systemIds = new Set<string>()
		// key to prevent re-rendering if alerts change but active alerts didn't
		const alertsKey: string[] = []

		for (const systemId of Object.keys(alerts)) {
			for (const alert of alerts[systemId].values()) {
				if (alert.triggered && alert.name in alertInfo) {
					activeAlerts.push(alert)
					systemIds.add(alert.system)
					alertsKey.push(`${alert.id}${alert.value}${alert.min}`)
				}
			}
		}

		return { activeAlerts, systemCount: systemIds.size, alertsKey: alertsKey.join("") }
	}, [alerts])

	return useMemo(() => {
		const monitorCount = downMonitors.length
		const alertCount = activeAlerts.length + monitorCount
		if (alertCount === 0) {
			return null
		}
		// name the alert directly in the banner when there is only one
		const [firstAlert] = activeAlerts
		const [firstMonitor] = downMonitors
		const monitorsHref = getPagePath($router, "monitors")
		let description: React.ReactNode
		if (alertCount === 1) {
			description = firstAlert ? <AlertTriggeredDesc alert={firstAlert} /> : <MonitorDownDesc monitor={firstMonitor} />
		} else if (!monitorCount) {
			description = <Plural value={systemCount} one="Across # system" other="Across # systems" />
		} else if (!systemCount) {
			description = <Plural value={monitorCount} one="# monitor is down" other="# monitors are down" />
		} else {
			description = (
				<>
					<Plural value={systemCount} one="Across # system" other="Across # systems" />
					{" · "}
					<Plural value={monitorCount} one="# monitor is down" other="# monitors are down" />
				</>
			)
		}
		return (
			<AlertBannerSheet
				open={open}
				onOpenChange={setOpen}
				className={className}
				title={
					alertCount === 1 ? (
						firstAlert ? (
							<AlertLabel alert={firstAlert} systemName={systems[firstAlert.system]?.name} />
						) : (
							<MonitorDownLabel monitor={firstMonitor} />
						)
					) : (
						<Plural value={alertCount} one="# active alert" other="# active alerts" />
					)
				}
				description={description}
				buttonLabel={<Trans>View alerts</Trans>}
				sheetTitle={<Trans>Active Alerts</Trans>}
				sheetDescription={
					<Plural value={alertCount} one="# alert is currently triggered" other="# alerts are currently triggered" />
				}
			>
				{downMonitors.map((monitor) => (
					<AlertBannerSheetItem
						key={monitor.id}
						href={monitorsHref}
						onClick={() => setOpen(false)}
						icon={monitorAlertInfo.MonitorDown.icon}
						title={<MonitorDownLabel monitor={monitor} />}
						description={<MonitorDownDesc monitor={monitor} />}
					/>
				))}
				{activeAlerts.map((alert) => {
					const info = alertInfo[alert.name as keyof typeof alertInfo]
					const system = systems[alert.system]
					return (
						<AlertBannerSheetItem
							key={alert.id}
							href={getPagePath($router, "system", { id: system?.id })}
							onClick={() => setOpen(false)}
							icon={info.icon}
							title={<AlertLabel alert={alert} systemName={system?.name} />}
							description={<AlertTriggeredDesc alert={alert} />}
						/>
					)
				})}
			</AlertBannerSheet>
		)
	}, [alertsKey, downMonitorsKey, systemCount, systems, open, className])
}
