import { t } from "@lingui/core/macro"
import { Plural, Trans } from "@lingui/react/macro"
import { SearchIcon } from "lucide-react"
import { useState } from "react"
import { getErrorMessage } from "@/components/network-monitors-table/monitor-form-utils"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"
import { toast } from "@/components/ui/use-toast"
import { pb } from "@/lib/api"
import { INCIDENT_IMPACTS, INCIDENT_STATUSES, MAX_INCIDENT_MESSAGE, MAX_INCIDENT_TITLE } from "@/lib/incidents"
import type { IncidentImpact, IncidentRecord, IncidentStatus } from "@/types"
import { incidentImpactLabel, incidentStatusLabel } from "./incident-ui"

export interface ChecklistItem {
	id: string
	name: string
	/** Secondary text shown next to the name. */
	detail?: string
}

/** Values to prefill a new incident with. */
export interface IncidentDraft {
	title?: string
	impact?: IncidentImpact
	monitors?: string[]
	systems?: string[]
	statusPages?: string[]
}

/** Create or edit an incident. New incidents can start with a first update. */
export function IncidentDialog({
	record,
	draft,
	monitors,
	systems,
	statusPages,
	onClose,
}: {
	record: IncidentRecord | null
	draft?: IncidentDraft
	monitors: ChecklistItem[]
	systems: ChecklistItem[]
	statusPages: ChecklistItem[]
	onClose: () => void
}) {
	const [title, setTitle] = useState(record?.title ?? draft?.title ?? "")
	const [impact, setImpact] = useState<IncidentImpact>(record?.impact ?? draft?.impact ?? "minor")
	const [status, setStatus] = useState<IncidentStatus>("investigating")
	const [message, setMessage] = useState("")
	const [selectedMonitors, setSelectedMonitors] = useState<string[]>(record?.monitors ?? draft?.monitors ?? [])
	const [selectedSystems, setSelectedSystems] = useState<string[]>(record?.systems ?? draft?.systems ?? [])
	const [selectedPages, setSelectedPages] = useState<string[]>(record?.statusPages ?? draft?.statusPages ?? [])
	const [saving, setSaving] = useState(false)

	const handleSubmit = async (e: React.FormEvent) => {
		e.preventDefault()
		const data = {
			user: pb.authStore.record?.id,
			title: title.trim(),
			impact,
			monitors: selectedMonitors,
			systems: selectedSystems,
			statusPages: selectedPages,
		}
		setSaving(true)
		try {
			if (record) {
				await pb.collection("incidents").update(record.id, data)
			} else {
				const created = await pb.collection<IncidentRecord>("incidents").create({ ...data, status })
				if (message.trim()) {
					await pb.collection("incident_updates").create({ incident: created.id, status, message: message.trim() })
				}
			}
			onClose()
		} catch (err) {
			toast({ variant: "destructive", title: t`Failed to save incident`, description: getErrorMessage(err) })
		} finally {
			setSaving(false)
		}
	}

	return (
		<DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto">
			<DialogHeader>
				<DialogTitle>{record ? <Trans>Edit incident</Trans> : <Trans>Create incident</Trans>}</DialogTitle>
				<DialogDescription>
					<Trans>Incidents are shown on the selected status pages with their updates.</Trans>
				</DialogDescription>
			</DialogHeader>
			<form onSubmit={handleSubmit} className="grid gap-4 min-w-0">
				<div className="grid gap-2">
					<Label htmlFor="incident-title">
						<Trans>Title</Trans>
					</Label>
					<Input
						id="incident-title"
						value={title}
						onChange={(e) => setTitle(e.target.value)}
						required
						maxLength={MAX_INCIDENT_TITLE}
					/>
				</div>
				<div className="grid gap-4 sm:grid-cols-2">
					<div className="grid gap-2">
						<Label htmlFor="incident-impact">
							<Trans>Impact</Trans>
						</Label>
						<Select value={impact} onValueChange={(value: IncidentImpact) => setImpact(value)}>
							<SelectTrigger id="incident-impact">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								{INCIDENT_IMPACTS.map((value) => (
									<SelectItem key={value} value={value}>
										{incidentImpactLabel(value)}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</div>
					{!record && <IncidentStatusSelect id="incident-status" value={status} onChange={setStatus} />}
				</div>
				{!record && (
					<div className="grid gap-2">
						<Label htmlFor="incident-message">
							<Trans>First update</Trans>
						</Label>
						<Textarea
							id="incident-message"
							value={message}
							onChange={(e) => setMessage(e.target.value)}
							rows={3}
							maxLength={MAX_INCIDENT_MESSAGE}
							placeholder={t`What is happening? Plain text; links are clickable.`}
						/>
					</div>
				)}
				<Checklist
					id="incident-pages"
					label={<Trans>Status pages</Trans>}
					description={<Trans>Pages that show this incident. Anyone who can view them sees its updates.</Trans>}
					items={statusPages}
					selected={selectedPages}
					onChange={setSelectedPages}
					emptyMessage={<Trans>No status pages.</Trans>}
				/>
				<Checklist
					id="incident-monitors"
					label={<Trans>Monitors</Trans>}
					items={monitors}
					selected={selectedMonitors}
					onChange={setSelectedMonitors}
					emptyMessage={<Trans>No monitors found.</Trans>}
				/>
				<Checklist
					id="incident-systems"
					label={<Trans>Systems</Trans>}
					items={systems}
					selected={selectedSystems}
					onChange={setSelectedSystems}
					emptyMessage={<Trans>No systems found.</Trans>}
				/>
				<DialogFooter>
					<Button type="button" variant="outline" onClick={onClose}>
						<Trans>Cancel</Trans>
					</Button>
					<Button type="submit" disabled={saving}>
						{record ? <Trans>Update</Trans> : <Trans>Create</Trans>}
					</Button>
				</DialogFooter>
			</form>
		</DialogContent>
	)
}

function IncidentStatusSelect({
	id,
	value,
	onChange,
}: {
	id: string
	value: IncidentStatus
	onChange: (value: IncidentStatus) => void
}) {
	return (
		<div className="grid gap-2">
			<Label htmlFor={id}>
				<Trans>Status</Trans>
			</Label>
			<Select value={value} onValueChange={(next: IncidentStatus) => onChange(next)}>
				<SelectTrigger id={id}>
					<SelectValue />
				</SelectTrigger>
				<SelectContent>
					{INCIDENT_STATUSES.map((status) => (
						<SelectItem key={status} value={status}>
							{incidentStatusLabel(status)}
						</SelectItem>
					))}
				</SelectContent>
			</Select>
		</div>
	)
}

/** Post an update to an incident's timeline, which also sets its status. */
export function IncidentUpdateDialog({
	incident,
	initialStatus,
	onClose,
}: {
	incident: IncidentRecord
	initialStatus: IncidentStatus
	onClose: () => void
}) {
	const [status, setStatus] = useState<IncidentStatus>(initialStatus)
	const [message, setMessage] = useState("")
	const [saving, setSaving] = useState(false)

	const handleSubmit = async (e: React.FormEvent) => {
		e.preventDefault()
		setSaving(true)
		try {
			await pb.collection("incident_updates").create({ incident: incident.id, status, message: message.trim() })
			onClose()
		} catch (err) {
			toast({ variant: "destructive", title: t`Failed to post update`, description: getErrorMessage(err) })
		} finally {
			setSaving(false)
		}
	}

	return (
		<DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto">
			<DialogHeader>
				<DialogTitle>
					{initialStatus === "resolved" ? <Trans>Resolve incident</Trans> : <Trans>Post update</Trans>}
				</DialogTitle>
				<DialogDescription className="break-words">{incident.title}</DialogDescription>
			</DialogHeader>
			<form onSubmit={handleSubmit} className="grid gap-4 min-w-0">
				<IncidentStatusSelect id="incident-update-status" value={status} onChange={setStatus} />
				<div className="grid gap-2">
					<Label htmlFor="incident-update-message">
						<Trans>Message</Trans>
					</Label>
					<Textarea
						id="incident-update-message"
						value={message}
						onChange={(e) => setMessage(e.target.value)}
						rows={4}
						required
						maxLength={MAX_INCIDENT_MESSAGE}
						placeholder={t`Plain text; links are clickable.`}
					/>
				</div>
				<DialogFooter>
					<Button type="button" variant="outline" onClick={onClose}>
						<Trans>Cancel</Trans>
					</Button>
					<Button type="submit" disabled={saving || !message.trim()}>
						<Trans>Post update</Trans>
					</Button>
				</DialogFooter>
			</form>
		</DialogContent>
	)
}

/** Searchable checkbox list. */
function Checklist({
	id,
	label,
	description,
	items,
	selected,
	onChange,
	emptyMessage,
}: {
	id: string
	label: React.ReactNode
	description?: React.ReactNode
	items: ChecklistItem[]
	selected: string[]
	onChange: (ids: string[]) => void
	emptyMessage: React.ReactNode
}) {
	const [search, setSearch] = useState("")
	const selectedSet = new Set(selected)
	const query = search.trim().toLocaleLowerCase()
	const filtered = items
		.filter((item) => !query || `${item.name} ${item.detail ?? ""}`.toLocaleLowerCase().includes(query))
		.sort((a, b) => a.name.localeCompare(b.name))
	const missing = selected.filter((itemId) => !items.some((item) => item.id === itemId)).length
	const toggle = (itemId: string, checked: boolean) => {
		onChange(checked ? [...selected, itemId] : selected.filter((s) => s !== itemId))
	}

	return (
		<div className="grid gap-2 min-w-0">
			<span className="text-sm font-medium leading-none">{label}</span>
			{description && <p className="-mt-1 text-xs text-muted-foreground">{description}</p>}
			<div className="rounded-md border min-w-0">
				{items.length > 6 && (
					<div className="flex items-center gap-2 border-b px-3">
						<SearchIcon aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
						<Input
							value={search}
							aria-label={t`Search`}
							onChange={(e) => setSearch(e.target.value)}
							placeholder={t`Search`}
							className="h-9 min-w-0 rounded-none border-0 bg-transparent px-0 shadow-none focus-visible:ring-0 focus-visible:ring-offset-0"
							onKeyDown={(e) => e.key === "Enter" && e.preventDefault()}
						/>
					</div>
				)}
				<div className="max-h-40 overflow-y-auto py-1">
					{filtered.length === 0 && <p className="px-3 py-2 text-sm text-muted-foreground">{emptyMessage}</p>}
					{filtered.map((item) => {
						const checkboxId = `${id}-${item.id}`
						return (
							<label
								key={item.id}
								htmlFor={checkboxId}
								className="flex items-center gap-2.5 px-3 py-1.5 cursor-pointer hover:bg-accent/50"
							>
								<Checkbox
									id={checkboxId}
									checked={selectedSet.has(item.id)}
									onCheckedChange={(checked) => toggle(item.id, checked === true)}
								/>
								<span className="min-w-0 truncate text-sm">{item.name}</span>
								{item.detail && <span className="min-w-0 truncate text-xs text-muted-foreground">{item.detail}</span>}
							</label>
						)
					})}
					{missing > 0 && (
						<p className="px-3 py-1.5 text-xs text-muted-foreground">
							<Plural value={missing} one="# selected item is unavailable" other="# selected items are unavailable" />
						</p>
					)}
				</div>
			</div>
		</div>
	)
}
