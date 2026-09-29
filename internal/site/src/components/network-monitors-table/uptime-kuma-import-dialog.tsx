import { Trans, useLingui } from "@lingui/react/macro"
import { useStore } from "@nanostores/react"
import { FileJsonIcon, LoaderCircleIcon } from "lucide-react"
import { useMemo, useRef, useState } from "react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { useToast } from "@/components/ui/use-toast"
import { pb } from "@/lib/api"
import { $systems } from "@/lib/stores"
import { cn, supportsNetworkMonitors } from "@/lib/utils"
import { getErrorMessage } from "./monitor-form-utils"

/** Largest backup file accepted client side. */
const MAX_BACKUP_BYTES = 10 * 1024 * 1024

const HUB = "hub"

/** Response of POST /api/beszel/import/uptime-kuma. */
interface UptimeKumaImportResult {
	created: number
	skipped: { name: string; reason: string }[] | null
	adjusted: { name: string; change: string }[] | null
	monitors: { name: string; protocol: string; target: string }[] | null
}

type PreviewRow = {
	key: string
	kind: "create" | "adjusted" | "skipped"
	name: string
	detail: string
	note: string
}

function previewRows(result: UptimeKumaImportResult): PreviewRow[] {
	const adjusted = new Map<string, string[]>()
	for (const { name, change } of result.adjusted ?? []) {
		adjusted.set(name, [...(adjusted.get(name) ?? []), change])
	}
	const rows: PreviewRow[] = []
	for (const [i, monitor] of (result.monitors ?? []).entries()) {
		const changes = adjusted.get(monitor.name)
		adjusted.delete(monitor.name)
		rows.push({
			key: `m${i}`,
			kind: changes ? "adjusted" : "create",
			name: monitor.name,
			detail: [monitor.protocol.toUpperCase(), monitor.target].filter(Boolean).join(" · "),
			note: changes?.join("; ") ?? "",
		})
	}
	// adjustments of monitors not listed above
	for (const [name, changes] of adjusted) {
		rows.push({ key: `a${name}`, kind: "adjusted", name, detail: "", note: changes.join("; ") })
	}
	for (const [i, { name, reason }] of (result.skipped ?? []).entries()) {
		rows.push({ key: `s${i}`, kind: "skipped", name, detail: "", note: reason })
	}
	return rows
}

/** Dialog that imports monitors from an Uptime Kuma backup, previewing the result with a dry run first. */
export function UptimeKumaImportDialog({
	open,
	setOpen,
	systemId,
}: {
	open: boolean
	setOpen: (open: boolean) => void
	/** Preselected agent system (on a system page); defaults to the hub. */
	systemId?: string
}) {
	const { t } = useLingui()
	const { toast } = useToast()
	const systems = useStore($systems)
	const eligibleSystems = useMemo(() => systems.filter(supportsNetworkMonitors), [systems])
	const [runOn, setRunOn] = useState(systemId || HUB)
	const [fileName, setFileName] = useState("")
	const [backup, setBackup] = useState<object | null>(null)
	const [fileError, setFileError] = useState("")
	const [preview, setPreview] = useState<UptimeKumaImportResult | null>(null)
	const [loading, setLoading] = useState(false)
	const fileInputRef = useRef<HTMLInputElement>(null)

	const rows = useMemo(() => (preview ? previewRows(preview) : []), [preview])

	function reset() {
		setRunOn(systemId || HUB)
		setFileName("")
		setBackup(null)
		setFileError("")
		setPreview(null)
		if (fileInputRef.current) {
			fileInputRef.current.value = ""
		}
	}

	async function handleFile(file: File | undefined) {
		setPreview(null)
		setBackup(null)
		setFileError("")
		setFileName(file?.name ?? "")
		if (!file) {
			return
		}
		if (file.size > MAX_BACKUP_BYTES) {
			setFileError(t`The file is too large. The maximum size is 10 MB.`)
			return
		}
		try {
			const parsed = JSON.parse(await file.text())
			if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
				throw new Error("not an object")
			}
			setBackup(parsed)
		} catch {
			setFileError(t`The file is not a valid Uptime Kuma backup (JSON).`)
		}
	}

	async function runImport(dryRun: boolean) {
		if (!backup) {
			return
		}
		setLoading(true)
		try {
			const result = await pb.send<UptimeKumaImportResult>("/api/beszel/import/uptime-kuma", {
				method: "POST",
				body: { backup, dryRun, system: runOn === HUB ? "" : runOn },
			})
			if (dryRun) {
				setPreview(result)
				return
			}
			const created = result.created
			const skipped = result.skipped?.length ?? 0
			toast({
				title: t`Import complete`,
				description: t`${created} monitor(s) created, ${skipped} skipped.`,
			})
			setOpen(false)
			reset()
		} catch (err) {
			toast({ variant: "destructive", title: t`Import failed`, description: getErrorMessage(err) })
		} finally {
			setLoading(false)
		}
	}

	const kindLabels = {
		create: t({ message: "Create", comment: "Import preview: monitor will be created" }),
		adjusted: t({ message: "Adjusted", comment: "Import preview: monitor will be created with changes" }),
		skipped: t({ message: "Skipped", comment: "Import preview: monitor will not be imported" }),
	}
	const kindClasses = {
		create: "bg-green-500/15! text-green-700 dark:text-green-400",
		adjusted: "bg-yellow-500/15! text-yellow-700 dark:text-yellow-400",
		skipped: "bg-muted! text-muted-foreground",
	}

	return (
		<Dialog
			open={open}
			onOpenChange={(nextOpen) => {
				setOpen(nextOpen)
				if (!nextOpen) {
					reset()
				}
			}}
		>
			<DialogContent className="max-w-2xl max-h-[calc(100dvh-2rem)] overflow-y-auto">
				<DialogHeader>
					<DialogTitle>
						<Trans>Import from Uptime Kuma</Trans>
					</DialogTitle>
					<DialogDescription>
						<Trans>
							Upload an Uptime Kuma backup (Settings → Backup → Export in Uptime Kuma). You'll see a preview before
							anything is created.
						</Trans>
					</DialogDescription>
				</DialogHeader>
				<div className="grid gap-4">
					<div className="grid gap-2">
						<Label htmlFor="kuma-backup-file">
							<Trans>Backup file</Trans>
						</Label>
						<Input
							id="kuma-backup-file"
							ref={fileInputRef}
							type="file"
							accept=".json,application/json"
							disabled={loading}
							onChange={(e) => handleFile(e.target.files?.[0])}
						/>
						{fileError ? (
							<p className="text-xs text-red-500">{fileError}</p>
						) : (
							<p className="text-xs text-muted-foreground">
								{fileName && backup ? (
									<span className="inline-flex items-center gap-1">
										<FileJsonIcon className="size-3.5" />
										{fileName}
									</span>
								) : (
									<Trans>JSON file, up to 10 MB.</Trans>
								)}
							</p>
						)}
					</div>
					<div className="grid gap-2">
						<Label htmlFor="kuma-run-on">
							<Trans>Runs on</Trans>
						</Label>
						<Select
							value={runOn}
							onValueChange={(value) => {
								setRunOn(value)
								setPreview(null)
							}}
							disabled={loading}
						>
							<SelectTrigger id="kuma-run-on">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								<SelectItem value={HUB}>
									<Trans>Hub</Trans>
								</SelectItem>
								{eligibleSystems.map((system) => (
									<SelectItem key={system.id} value={system.id}>
										{system.name}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<p className="text-xs text-muted-foreground">
							{runOn === HUB ? (
								<Trans>The hub checks the imported monitors. Only you can see them.</Trans>
							) : (
								<Trans>The selected agent checks the imported monitors.</Trans>
							)}
						</p>
					</div>
					{preview && (
						<div className="grid gap-2">
							<p className="text-sm font-medium">
								<Trans>
									Preview: {preview.created} to create, {preview.adjusted?.length ?? 0} adjusted,{" "}
									{preview.skipped?.length ?? 0} skipped
								</Trans>
							</p>
							{rows.length ? (
								<div className="max-h-80 overflow-auto rounded-md border">
									<Table className="text-sm">
										<TableHeader className="sticky top-0 bg-card z-10">
											<TableRow>
												<TableHead className="w-24">
													<Trans>Result</Trans>
												</TableHead>
												<TableHead>
													<Trans>Name</Trans>
												</TableHead>
												<TableHead>
													<Trans>Details</Trans>
												</TableHead>
											</TableRow>
										</TableHeader>
										<TableBody>
											{rows.map((row) => (
												<TableRow key={row.key}>
													<TableCell className="align-top py-2">
														<Badge className={cn("font-normal", kindClasses[row.kind])}>{kindLabels[row.kind]}</Badge>
													</TableCell>
													<TableCell className="align-top py-2 break-all">{row.name}</TableCell>
													<TableCell className="align-top py-2 text-muted-foreground">
														{row.detail && <div className="break-all">{row.detail}</div>}
														{row.note && (
															<div className={cn(row.kind === "adjusted" && "text-yellow-700 dark:text-yellow-400")}>
																{row.note}
															</div>
														)}
													</TableCell>
												</TableRow>
											))}
										</TableBody>
									</Table>
								</div>
							) : (
								<p className="text-sm text-muted-foreground">
									<Trans>The backup contains no monitors.</Trans>
								</p>
							)}
						</div>
					)}
				</div>
				<DialogFooter>
					{preview ? (
						<>
							<Button variant="outline" onClick={() => setPreview(null)} disabled={loading}>
								<Trans>Back</Trans>
							</Button>
							<Button onClick={() => runImport(false)} disabled={loading || !preview.created}>
								{loading && <LoaderCircleIcon className="size-4 me-2 animate-spin" />}
								<Trans>Import {preview.created} monitor(s)</Trans>
							</Button>
						</>
					) : (
						<Button onClick={() => runImport(true)} disabled={loading || !backup}>
							{loading && <LoaderCircleIcon className="size-4 me-2 animate-spin" />}
							<Trans>Preview import</Trans>
						</Button>
					)}
				</DialogFooter>
			</DialogContent>
		</Dialog>
	)
}
