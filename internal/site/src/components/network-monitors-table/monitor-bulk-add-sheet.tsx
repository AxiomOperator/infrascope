import { useEffect, useRef, useState } from "react"
import { Trans, useLingui } from "@lingui/react/macro"
import { pb } from "@/lib/api"
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"
import { useToast } from "@/components/ui/use-toast"
import type { NetworkMonitorRecord } from "@/types"
import {
	getErrorMessage,
	getMonitorIdentityKey,
	hubMinInterval,
	isBulkPushLine,
	parseBulkMonitorLine,
} from "./monitor-form-utils"
import { SystemMultiSelect } from "./system-multi-select"

export type BulkRunsOn = "hub" | "agent"

/** Sheet that creates monitors from CSV-like lines on the hub or on one or more agent systems. */
export function MonitorBulkAddSheet({
	open,
	setOpen,
	systemId,
	monitors,
	selectedSystemIds,
	setSelectedSystemIds,
	initialRunsOn = "agent",
	systemName,
}: {
	open: boolean
	setOpen: (open: boolean) => void
	systemId?: string
	monitors: NetworkMonitorRecord[]
	selectedSystemIds: Set<string>
	setSelectedSystemIds: (ids: Set<string>) => void
	/** Runner selected when the sheet opens. */
	initialRunsOn?: BulkRunsOn
	/** Name of the current system (on a system page). */
	systemName?: string
}) {
	const [bulkInput, setBulkInput] = useState("")
	const [bulkLoading, setBulkLoading] = useState(false)
	const [runsOn, setRunsOn] = useState<BulkRunsOn>(initialRunsOn)
	const bulkFormRef = useRef<HTMLFormElement>(null)
	const { toast } = useToast()
	const { t } = useLingui()
	const isHub = runsOn === "hub"

	useEffect(() => {
		if (open) {
			setRunsOn(initialRunsOn)
		}
	}, [open, initialRunsOn])

	async function handleBulkSubmit(e: React.FormEvent) {
		e.preventDefault()
		setBulkLoading(true)
		let closedForSubmit = false

		try {
			// hub monitors have no system
			const targetSystems = isHub ? [""] : systemId ? [systemId] : Array.from(selectedSystemIds)
			if (!targetSystems.length) {
				throw new Error(t`Select at least one system.`)
			}
			const allLines = bulkInput
				.split(/\r?\n/)
				.map((line, index) => ({ line, lineNumber: index + 1 }))
				.filter(({ line }) => line.trim())
			// push monitors have no target and are created one at a time
			const rawLines = allLines.filter(({ line }) => !isBulkPushLine(line))
			const skippedPush = allLines.length - rawLines.length
			if (!rawLines.length) {
				throw new Error(
					skippedPush ? t`Push monitors can't be bulk added. Add them individually.` : t`Enter at least one monitor.`
				)
			}
			const userId = pb.authStore.record?.id ?? ""
			if (isHub && !userId) {
				throw new Error(t`You must be logged in to add hub monitors.`)
			}

			// validate every line before creating anything
			const payloadsBySystem = targetSystems.map((system) => {
				const payloads = rawLines.map(({ line, lineNumber }) => {
					const payload = parseBulkMonitorLine(line, lineNumber, system)
					if (isHub && payload.interval < hubMinInterval) {
						throw new Error(
							t`Line ${lineNumber}: hub monitors must use an interval of at least ${hubMinInterval} seconds.`
						)
					}
					return isHub ? { ...payload, users: [userId] } : payload
				})
				return { system, payloads }
			})

			let totalCreated = 0
			closedForSubmit = true

			for (const { system, payloads } of payloadsBySystem) {
				const existingMonitorKeys = new Set(
					monitors.filter((monitor) => monitor.system === system).map((monitor) => getMonitorIdentityKey(monitor))
				)
				const newPayloads: typeof payloads = []

				for (const payload of payloads) {
					const monitorKey = getMonitorIdentityKey(payload)
					if (existingMonitorKeys.has(monitorKey)) {
						continue
					}
					existingMonitorKeys.add(monitorKey)
					newPayloads.push(payload)
				}

				if (!newPayloads.length) continue

				let batch = pb.createBatch()
				let inBatch = 0
				for (const payload of newPayloads) {
					batch.collection("network_monitors").create(payload)
					inBatch++
					if (inBatch > 20) {
						await batch.send()
						batch = pb.createBatch()
						inBatch = 0
					}
				}
				if (inBatch) {
					await batch.send()
				}
				totalCreated += newPayloads.length
			}

			if (!totalCreated) {
				throw new Error(t`No new monitors. All entries already exist.`)
			}

			setBulkInput("")
			const description = skippedPush
				? t`${totalCreated} monitor(s) added. ${skippedPush} push line(s) skipped.`
				: t`${totalCreated} monitor(s) added.`
			toast({ title: t`Monitors created`, description })
		} catch (err: unknown) {
			if (closedForSubmit) {
				setOpen(true)
			}
			toast({ variant: "destructive", title: t`Error`, description: getErrorMessage(err) })
		} finally {
			setBulkLoading(false)
		}
	}

	return (
		<Sheet
			open={open}
			onOpenChange={(nextOpen) => {
				setOpen(nextOpen)
				if (!nextOpen) {
					setBulkInput("")
				}
			}}
		>
			<SheetContent className="w-full sm:max-w-xl gap-0">
				<SheetHeader className="border-b">
					<SheetTitle>
						<Trans>Bulk Add {{ foo: t`Network Monitors` }}</Trans>
					</SheetTitle>
					<SheetDescription>
						<Trans>target[,protocol[,port[,interval[,server]]]]</Trans>
					</SheetDescription>
				</SheetHeader>
				<form ref={bulkFormRef} onSubmit={handleBulkSubmit} className="flex h-full flex-col overflow-hidden">
					<div className="flex-1 flex flex-col space-y-4 overflow-auto p-4">
						<div className="grid gap-2">
							<Label htmlFor="bulk-monitor-runs-on">
								<Trans>Runs on</Trans>
							</Label>
							<Select value={runsOn} onValueChange={(value) => setRunsOn(value as BulkRunsOn)} disabled={bulkLoading}>
								<SelectTrigger id="bulk-monitor-runs-on" className="bg-card">
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									<SelectItem value="hub">
										<Trans>Hub</Trans>
									</SelectItem>
									<SelectItem value="agent">{systemId ? systemName || t`This system` : t`Agent`}</SelectItem>
								</SelectContent>
							</Select>
						</div>
						{!isHub && !systemId && (
							<div className="grid gap-2">
								<Label htmlFor="bulk-monitor-systems" className="sr-only">
									<Trans>Systems</Trans>
								</Label>
								<SystemMultiSelect
									id="bulk-monitor-systems"
									selectedSystemIds={selectedSystemIds}
									onChange={setSelectedSystemIds}
									disabled={bulkLoading}
									className="bg-card"
								/>
							</div>
						)}
						<div className="grow flex flex-col gap-2">
							<Label htmlFor="bulk-monitors" className="sr-only">
								Entries
							</Label>
							<Textarea
								id="bulk-monitors"
								value={bulkInput}
								onChange={(e) => setBulkInput(e.target.value)}
								onKeyDown={(e) => {
									if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
										e.preventDefault()
										bulkFormRef.current?.requestSubmit()
									}
								}}
								className="font-mono grow text-sm bg-card"
								placeholder={[
									"1.1.1.1",
									"example.com,tcp",
									"https://example.com,http,,60",
									"example.com,dns,,,1.1.1.1",
								].join("\n")}
								required
							/>
							<p className="text-xs text-muted-foreground">
								<Trans>target[,protocol[,port[,interval[,server]]]]</Trans>
							</p>
							<p className="text-xs text-muted-foreground">
								{isHub ? (
									<Trans>
										Bulk added monitors are checked by the hub (minimum interval {hubMinInterval}s). Only you can see
										them. Push lines are skipped.
									</Trans>
								) : (
									<Trans>Bulk added monitors run on the selected agents. Push lines are skipped.</Trans>
								)}
							</p>
						</div>
					</div>
					<SheetFooter className="border-t">
						<Button type="submit" disabled={bulkLoading || (!isHub && !systemId && !selectedSystemIds.size)}>
							<Trans>Add {{ foo: t`Network Monitors` }}</Trans>
						</Button>
					</SheetFooter>
				</form>
			</SheetContent>
		</Sheet>
	)
}
