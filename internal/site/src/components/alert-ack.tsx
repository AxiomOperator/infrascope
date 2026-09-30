import { t } from "@lingui/core/macro"
import { Trans } from "@lingui/react/macro"
import { CheckCheckIcon, CheckIcon, LoaderCircleIcon, MessageSquareTextIcon, Trash2Icon, UndoIcon } from "lucide-react"
import { useEffect, useMemo, useState } from "react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import { toast } from "@/components/ui/use-toast"
import { ACK_NOTE_MAX_CHARS, indexOpenHistory, isAcknowledged, takeAckParam } from "@/lib/alert-ack"
import { isReadOnlyUser, pb } from "@/lib/api"
import { formatRelativeTime } from "@/lib/network-monitor-utils"
import { cn, formatShortDate } from "@/lib/utils"
import type { AlertNoteRecord, AlertsHistoryRecord } from "@/types"

/** Acknowledges an alert history row, optionally with a note. */
export function acknowledgeAlert(id: string, note = "") {
	return pb.send<Partial<AlertsHistoryRecord>>(`/api/beszel/alerts-history/${encodeURIComponent(id)}/ack`, {
		method: "POST",
		body: { note },
	})
}

/** Clears the acknowledgement of an alert history row. */
export function unacknowledgeAlert(id: string) {
	return pb.send<Partial<AlertsHistoryRecord>>(`/api/beszel/alerts-history/${encodeURIComponent(id)}/unack`, {
		method: "POST",
	})
}

function showError(e: unknown) {
	toast({ variant: "destructive", title: t`Error`, description: (e as Error)?.message })
}

/** Display name of a user id; alerts are only acknowledged by their own user. */
function userLabel(id?: string) {
	const me = pb.authStore.record
	if (me && id === me.id) {
		return me.name || me.username || me.email || t`you`
	}
	return id || ""
}

/** "Acknowledged by X · time" badge, or nothing for unacknowledged rows. */
export function AckBadge({ record, className }: { record: AlertsHistoryRecord; className?: string }) {
	if (!record.acknowledgedAt) {
		return null
	}
	const by = userLabel(record.acknowledgedBy)
	const when = formatRelativeTime(record.acknowledgedAt)
	return (
		<Badge
			variant="outline"
			className={cn("gap-1 font-normal text-muted-foreground pointer-events-none", className)}
			title={`${record.acknowledgedAt} UTC`}
		>
			<CheckCheckIcon className="size-3" />
			<Trans>
				Acknowledged by {by} · {when}
			</Trans>
		</Badge>
	)
}

/** Acknowledge (with an optional note) and unacknowledge controls of a history row. */
export function AckControls({
	record,
	onChange,
	compact = false,
}: {
	record: AlertsHistoryRecord
	onChange?: (record: AlertsHistoryRecord) => void
	compact?: boolean
}) {
	const [note, setNote] = useState("")
	const [showNote, setShowNote] = useState(false)
	const [busy, setBusy] = useState(false)
	const readOnly = isReadOnlyUser()
	const acknowledged = isAcknowledged(record)

	async function run(action: () => Promise<Partial<AlertsHistoryRecord>>) {
		setBusy(true)
		try {
			const result = await action()
			onChange?.({ ...record, ...result })
			setNote("")
			setShowNote(false)
		} catch (e) {
			showError(e)
		}
		setBusy(false)
	}

	return (
		<div className="grid gap-2">
			<div className="flex flex-wrap items-center gap-2">
				<AckBadge record={record} />
				{!readOnly &&
					(acknowledged ? (
						<Button
							size="sm"
							variant="ghost"
							className="h-7 px-2"
							disabled={busy}
							onClick={() => run(() => unacknowledgeAlert(record.id))}
						>
							<UndoIcon className="size-3.5 me-1" />
							<Trans>Unacknowledge</Trans>
						</Button>
					) : (
						<>
							<Button
								size="sm"
								variant="outline"
								className="h-7 px-2"
								disabled={busy}
								onClick={() => run(() => acknowledgeAlert(record.id, note.trim()))}
							>
								{busy ? (
									<LoaderCircleIcon className="size-3.5 me-1 animate-spin" />
								) : (
									<CheckIcon className="size-3.5 me-1" />
								)}
								<Trans>Acknowledge</Trans>
							</Button>
							{!showNote && !compact && (
								<Button size="sm" variant="ghost" className="h-7 px-2" onClick={() => setShowNote(true)}>
									<Trans>Add note</Trans>
								</Button>
							)}
						</>
					))}
			</div>
			{record.ackNote && <p className="text-sm text-muted-foreground whitespace-pre-wrap">{record.ackNote}</p>}
			{showNote && !acknowledged && (
				<Textarea
					value={note}
					maxLength={ACK_NOTE_MAX_CHARS}
					onChange={(e) => setNote(e.target.value)}
					placeholder={t`Optional acknowledgement note`}
					aria-label={t`Acknowledgement note`}
				/>
			)}
		</div>
	)
}

/** Notes of a history row, with a form to add one. */
export function AlertNotes({ alertId }: { alertId: string }) {
	const [notes, setNotes] = useState<AlertNoteRecord[]>([])
	const [text, setText] = useState("")
	const [busy, setBusy] = useState(false)
	const readOnly = isReadOnlyUser()
	const me = pb.authStore.record?.id

	useEffect(() => {
		let cancelled = false
		pb.collection<AlertNoteRecord>("alert_notes")
			.getFullList({ filter: pb.filter("alert = {:alert}", { alert: alertId }), sort: "created" })
			.then((items) => {
				if (!cancelled) setNotes(items)
			})
			.catch(() => {})
		return () => {
			cancelled = true
		}
	}, [alertId])

	async function addNote() {
		const value = text.trim()
		if (!value || !me) return
		setBusy(true)
		try {
			const note = await pb
				.collection<AlertNoteRecord>("alert_notes")
				.create({ alert: alertId, author: me, text: value })
			setNotes((current) => [...current, note])
			setText("")
		} catch (e) {
			showError(e)
		}
		setBusy(false)
	}

	async function deleteNote(id: string) {
		try {
			await pb.collection("alert_notes").delete(id)
			setNotes((current) => current.filter((n) => n.id !== id))
		} catch (e) {
			showError(e)
		}
	}

	return (
		<div className="grid gap-2">
			<h4 className="text-sm font-medium">
				<Trans>Notes</Trans>
			</h4>
			{notes.length === 0 ? (
				<p className="text-sm text-muted-foreground">
					<Trans>No notes yet.</Trans>
				</p>
			) : (
				<ul className="grid gap-2">
					{notes.map((note) => (
						<li key={note.id} className="rounded-md border p-2 text-sm">
							<div className="flex items-center gap-2 text-xs text-muted-foreground">
								<span>{userLabel(note.author)}</span>
								<span title={`${note.created} UTC`}>{formatShortDate(note.created)}</span>
								{!readOnly && note.author === me && (
									<Button
										size="icon"
										variant="ghost"
										className="size-6 ms-auto"
										onClick={() => deleteNote(note.id)}
										aria-label={t`Delete note`}
									>
										<Trash2Icon className="size-3.5" />
									</Button>
								)}
							</div>
							<p className="mt-1 whitespace-pre-wrap break-words">{note.text}</p>
						</li>
					))}
				</ul>
			)}
			{!readOnly && (
				<div className="grid gap-2">
					<Textarea
						value={text}
						maxLength={ACK_NOTE_MAX_CHARS}
						onChange={(e) => setText(e.target.value)}
						placeholder={t`Add a note...`}
						aria-label={t`Add a note`}
					/>
					<Button size="sm" className="justify-self-end" disabled={busy || !text.trim()} onClick={addNote}>
						<Trans>Add note</Trans>
					</Button>
				</div>
			)}
		</div>
	)
}

/** Acknowledgement and notes of a history row, for detail views. */
export function AlertAckPanel({
	record,
	onChange,
}: {
	record: AlertsHistoryRecord
	onChange?: (record: AlertsHistoryRecord) => void
}) {
	return (
		<div className="grid gap-4">
			<AckControls record={record} onChange={onChange} />
			<AlertNotes alertId={record.id} />
		</div>
	)
}

/** Open alert history rows of the user, indexed with indexOpenHistory; loaded and kept current while enabled. */
export function useOpenAlertHistory(enabled: boolean) {
	const [rows, setRows] = useState<AlertsHistoryRecord[]>([])

	useEffect(() => {
		if (!enabled) return
		let cancelled = false
		let unsubscribe: (() => void) | undefined
		const options = {
			fields: "id,alert_id,name,system,monitor,monitor_name,created,resolved,acknowledgedAt,acknowledgedBy,ackNote",
		}
		pb.collection<AlertsHistoryRecord>("alerts_history")
			.getFullList({ ...options, filter: "resolved = null", sort: "-created" })
			.then((items) => {
				if (!cancelled) setRows(items)
			})
			.catch(() => {})
		;(async () => {
			try {
				const unsub = await pb.collection<AlertsHistoryRecord>("alerts_history").subscribe(
					"*",
					(e) => {
						if (cancelled) return
						setRows((current) => {
							const rest = current.filter((r) => r.id !== e.record.id)
							return e.action === "delete" || e.record.resolved ? rest : [e.record, ...rest]
						})
					},
					options
				)
				if (cancelled) unsub()
				else unsubscribe = unsub
			} catch {
				// realtime is optional here
			}
		})()
		return () => {
			cancelled = true
			unsubscribe?.()
		}
	}, [enabled])

	return useMemo(() => indexOpenHistory(rows), [rows])
}

/** Compact acknowledgement controls and notes of an open alert, for the active alerts sheet. */
export function AlertSheetAck({ record }: { record: AlertsHistoryRecord }) {
	const [current, setCurrent] = useState(record)
	const [notesOpen, setNotesOpen] = useState(false)
	useEffect(() => setCurrent(record), [record])
	return (
		<div className="grid gap-2">
			<div className="flex flex-wrap items-start gap-2">
				<AckControls record={current} onChange={setCurrent} compact />
				<Button size="sm" variant="ghost" className="h-7 px-2 ms-auto" onClick={() => setNotesOpen((open) => !open)}>
					<MessageSquareTextIcon className="size-3.5 me-1" />
					<Trans>Notes</Trans>
				</Button>
			</div>
			{notesOpen && <AlertNotes alertId={current.id} />}
		</div>
	)
}

/** Shows a toast after the acknowledgement link redirect (?ack=1) and removes the parameter from the URL. */
export function showAckLinkToast() {
	const { acknowledged, url } = takeAckParam(window.location.href)
	if (url === window.location.href) return
	window.history.replaceState(window.history.state, "", url)
	if (acknowledged) {
		toast({ title: t`Alert acknowledged`, description: t`You will not get reminders for this alert.` })
	}
}
