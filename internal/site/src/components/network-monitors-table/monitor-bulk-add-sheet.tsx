import { useRef, useState } from "react"
import { Trans, useLingui } from "@lingui/react/macro"
import { pb } from "@/lib/api"
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { useToast } from "@/components/ui/use-toast"
import type { NetworkMonitorRecord } from "@/types"
import { getErrorMessage, getMonitorIdentityKey, parseBulkMonitorLine } from "./monitor-form-utils"
import { SystemMultiSelect } from "./system-multi-select"

/** Sheet that creates agent monitors from CSV-like lines on one or more systems. */
export function MonitorBulkAddSheet({
	open,
	setOpen,
	systemId,
	monitors,
	selectedSystemIds,
	setSelectedSystemIds,
}: {
	open: boolean
	setOpen: (open: boolean) => void
	systemId?: string
	monitors: NetworkMonitorRecord[]
	selectedSystemIds: Set<string>
	setSelectedSystemIds: (ids: Set<string>) => void
}) {
	const [bulkInput, setBulkInput] = useState("")
	const [bulkLoading, setBulkLoading] = useState(false)
	const bulkFormRef = useRef<HTMLFormElement>(null)
	const { toast } = useToast()
	const { t } = useLingui()

	async function handleBulkSubmit(e: React.FormEvent) {
		e.preventDefault()
		setBulkLoading(true)
		let closedForSubmit = false

		try {
			const targetSystems = systemId ? [systemId] : Array.from(selectedSystemIds)
			if (!targetSystems.length) {
				throw new Error("Select at least one system.")
			}
			const rawLines = bulkInput.split(/\r?\n/).filter((line) => line.trim())
			if (!rawLines.length) {
				throw new Error("Enter at least one monitor.")
			}

			let totalCreated = 0
			closedForSubmit = true

			for (const system of targetSystems) {
				const payloads = rawLines.map((line, index) => parseBulkMonitorLine(line, index + 1, system))
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
				throw new Error("No new monitors. All entries already exist.")
			}

			setBulkInput("")
			toast({ title: t`Monitors created`, description: `${totalCreated} monitor(s) added.` })
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
						{!systemId && (
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
								<Trans>Bulk added monitors run on the selected agents.</Trans>
							</p>
						</div>
					</div>
					<SheetFooter className="border-t">
						<Button type="submit" disabled={bulkLoading || (!systemId && !selectedSystemIds.size)}>
							<Trans>Add {{ foo: t`Network Monitors` }}</Trans>
						</Button>
					</SheetFooter>
				</form>
			</SheetContent>
		</Sheet>
	)
}
