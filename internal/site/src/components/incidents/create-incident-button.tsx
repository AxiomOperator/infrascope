import { t } from "@lingui/core/macro"
import { Trans } from "@lingui/react/macro"
import { useStore } from "@nanostores/react"
import { SirenIcon } from "lucide-react"
import { useEffect, useMemo, useState } from "react"
import { Button } from "@/components/ui/button"
import { Dialog } from "@/components/ui/dialog"
import { pb } from "@/lib/api"
import { getMonitorName, getMonitorTarget } from "@/lib/network-monitor-utils"
import { $systems } from "@/lib/stores"
import type { NetworkMonitorRecord, StatusPageRecord } from "@/types"
import { type ChecklistItem, IncidentDialog } from "./incident-dialog"

/**
 * Opens a new incident prefilled for a monitor: its name as the title, the
 * monitor as the component and the status pages that show it.
 */
export function CreateIncidentButton({ monitor }: { monitor: NetworkMonitorRecord }) {
	const [open, setOpen] = useState(false)
	const [pages, setPages] = useState<StatusPageRecord[] | null>(null)
	const systems = useStore($systems)

	useEffect(() => {
		if (!open || pages) return
		let cancelled = false
		pb.collection<StatusPageRecord>("status_pages")
			.getFullList({ sort: "title", fields: "id,title,slug,monitors" })
			.then((items) => !cancelled && setPages(items))
			.catch(() => !cancelled && setPages([]))
		return () => {
			cancelled = true
		}
	}, [open, pages])

	const name = getMonitorName(monitor)
	const target = getMonitorTarget(monitor)
	const monitorItems = useMemo<ChecklistItem[]>(
		() => [{ id: monitor.id, name, detail: monitor.name && target ? target : undefined }],
		[monitor.id, monitor.name, name, target]
	)
	const systemItems = useMemo<ChecklistItem[]>(() => systems.map((s) => ({ id: s.id, name: s.name })), [systems])
	const pageItems = useMemo<ChecklistItem[]>(
		() => (pages ?? []).map((p) => ({ id: p.id, name: p.title, detail: `/status/${p.slug}` })),
		[pages]
	)

	return (
		<>
			<Button variant="outline" size="sm" className="h-8" onClick={() => setOpen(true)}>
				<SirenIcon className="size-4" />
				<span className="ms-1">
					<Trans>Create incident</Trans>
				</span>
			</Button>
			<Dialog open={open} onOpenChange={setOpen}>
				{open && pages && (
					<IncidentDialog
						record={null}
						draft={{
							title: t`${name} is down`,
							impact: "major",
							monitors: [monitor.id],
							statusPages: pages.filter((p) => p.monitors?.includes(monitor.id)).map((p) => p.id),
						}}
						monitors={monitorItems}
						systems={systemItems}
						statusPages={pageItems}
						onClose={() => setOpen(false)}
					/>
				)}
			</Dialog>
		</>
	)
}
