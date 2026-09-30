import { useEffect, useState } from "react"
import { Trans } from "@lingui/react/macro"
import { Card } from "@/components/ui/card"
import { pb } from "@/lib/api"
import { parseSuppressedBy } from "@/lib/monitor-dependencies"
import { getMonitorName, getMonitorStatus, monitorStatusBgColors } from "@/lib/network-monitor-utils"
import { cn } from "@/lib/utils"
import type { NetworkMonitorRecord } from "@/types"
import type { DependencyMonitor } from "./monitor-dependency-select"
import { monitorStatusLabel } from "./monitor-status-badge"

/** The monitors a monitor depends on, with their status, in its detail sheet. */
export function MonitorDependenciesCard({
	monitor,
	enabled,
}: {
	monitor: Pick<NetworkMonitorRecord, "dependsOn" | "suppressedBy">
	enabled: boolean
}) {
	const ids = monitor.dependsOn ?? []
	const key = ids.join(",")
	const [parents, setParents] = useState<DependencyMonitor[]>([])
	// Refetch when the parents or their down state (suppressedBy) change.
	useEffect(() => {
		if (!enabled || !key) return
		let cancelled = false
		const filter = key
			.split(",")
			.map((id) => pb.filter("id = {:id}", { id }))
			.join(" || ")
		pb.collection<DependencyMonitor>("network_monitors")
			.getFullList({ filter, fields: "id,name,target,protocol,port,status,enabled,dependsOn", requestKey: null })
			.then((records) => {
				if (!cancelled) setParents(records)
			})
			.catch((error) => console.error("Failed to load dependencies", error))
		return () => {
			cancelled = true
		}
	}, [enabled, key, monitor.suppressedBy])

	if (!ids.length) return null
	const suppressed = parseSuppressedBy(monitor.suppressedBy)
	const byId = new Map(parents.map((parent) => [parent.id, parent]))
	return (
		<Card className="p-4 grid gap-2">
			<div className="flex flex-wrap items-center justify-between gap-2 text-sm">
				<span className="font-medium">
					<Trans>Depends on</Trans>
				</span>
				{suppressed.length > 0 && (
					<span className="text-muted-foreground">
						<Trans>Notifications suppressed while a parent is down</Trans>
					</span>
				)}
			</div>
			<ul className="grid divide-y rounded-md border text-sm">
				{ids.map((id) => {
					const parent = byId.get(id)
					const status = parent ? getMonitorStatus(parent) : ""
					return (
						<li key={id} className="flex flex-wrap items-center gap-x-2 gap-y-1 px-3 py-2">
							<span className={cn("size-2 shrink-0 rounded-full", monitorStatusBgColors[status])} />
							<span className="font-medium break-all">{parent ? getMonitorName(parent) : id}</span>
							<span className="ms-auto text-muted-foreground">{monitorStatusLabel(status)}</span>
						</li>
					)
				})}
			</ul>
		</Card>
	)
}
