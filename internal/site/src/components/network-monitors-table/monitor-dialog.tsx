import { useEffect, useRef, useState } from "react"
import { Trans, useLingui } from "@lingui/react/macro"
import { useStore } from "@nanostores/react"
import { ChevronDownIcon, ListIcon, PlusIcon, TriangleAlertIcon, UploadIcon } from "lucide-react"
import { pb } from "@/lib/api"
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "@/components/ui/dialog"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { useToast } from "@/components/ui/use-toast"
import { $allSystemsById, $systems } from "@/lib/stores"
import { supportsNetworkMonitors } from "@/lib/utils"
import { needsAgentUpdateForMonitorOptions } from "@/lib/network-monitor-utils"
import type { NetworkMonitorRecord } from "@/types"
import {
	buildMonitorPayload,
	defaultInterval,
	getErrorMessage,
	type HttpFormState,
	httpFormFromMonitor,
	httpPayloadFromForm,
	hubMinInterval,
	isHttpsTarget,
	type MonitorProtocol,
} from "./monitor-form-utils"
import { hasCustomHttpOptions, MonitorHttpOptions, SwitchField } from "./monitor-http-options"
import { MonitorPushUrl } from "./monitor-push-url"
import { MonitorBulkAddSheet } from "./monitor-bulk-add-sheet"
import { SystemMultiSelect } from "./system-multi-select"
import { UptimeKumaImportDialog } from "./uptime-kuma-import-dialog"

type RunsOn = "hub" | "agent"

export function AddMonitorDialog({ systemId, monitors }: { systemId?: string; monitors: NetworkMonitorRecord[] }) {
	const [open, setOpen] = useState(false)
	const [bulkOpen, setBulkOpen] = useState(false)
	const [importOpen, setImportOpen] = useState(false)
	const [bulkRunsOn, setBulkRunsOn] = useState<RunsOn>("agent")
	const [bulkSelectedSystemIds, setBulkSelectedSystemIds] = useState<Set<string>>(new Set())
	const { t } = useLingui()
	const systems = useStore($systems)
	const allSystems = useStore($allSystemsById)
	const hasEligibleSystems = systemId ? true : systems.some(supportsNetworkMonitors)

	const openBulkAdd = (selectedSystemIds?: Set<string>, runsOn?: RunsOn) => {
		if (!systemId && selectedSystemIds) {
			setBulkSelectedSystemIds(new Set(selectedSystemIds))
		}
		setBulkRunsOn(runsOn ?? (hasEligibleSystems ? "agent" : "hub"))
		setOpen(false)
		setBulkOpen(true)
	}

	const openAdd = () => {
		setBulkOpen(false)
		setOpen(true)
	}

	return (
		<>
			<div className="flex gap-0 rounded-lg">
				<Button variant="outline" onClick={openAdd} className="rounded-e-none grow">
					<PlusIcon className="size-4 me-1" />
					<span className="sm:hidden">
						<Trans>Add</Trans>
					</span>
					<span className="hidden sm:inline">
						<Trans>Add {{ foo: t`Monitor` }}</Trans>
					</span>
				</Button>
				<div className="w-px h-full bg-muted"></div>
				<DropdownMenu>
					<DropdownMenuTrigger asChild>
						<Button variant="outline" className="px-2 rounded-s-none border-s-0" aria-label={t`More actions`}>
							<ChevronDownIcon className="size-4" />
						</Button>
					</DropdownMenuTrigger>
					<DropdownMenuContent align="end">
						<DropdownMenuItem onClick={() => openBulkAdd()}>
							<ListIcon className="size-4 me-2" />
							<Trans>Bulk Add</Trans>
						</DropdownMenuItem>
						<DropdownMenuItem onClick={() => setImportOpen(true)}>
							<UploadIcon className="size-4 me-2" />
							<Trans>Import from Uptime Kuma</Trans>
						</DropdownMenuItem>
					</DropdownMenuContent>
				</DropdownMenu>
			</div>
			<Dialog open={open} onOpenChange={setOpen}>
				<MonitorDialogContent open={open} setOpen={setOpen} systemId={systemId} onOpenBulkAdd={openBulkAdd} />
			</Dialog>
			<MonitorBulkAddSheet
				open={bulkOpen}
				setOpen={setBulkOpen}
				systemId={systemId}
				systemName={systemId ? allSystems[systemId]?.name : undefined}
				monitors={monitors}
				selectedSystemIds={bulkSelectedSystemIds}
				setSelectedSystemIds={setBulkSelectedSystemIds}
				initialRunsOn={bulkRunsOn}
			/>
			<UptimeKumaImportDialog open={importOpen} setOpen={setImportOpen} systemId={systemId} />
		</>
	)
}

export function EditMonitorDialog({
	open,
	setOpen,
	systemId,
	monitor,
}: {
	open: boolean
	setOpen: (open: boolean) => void
	systemId?: string
	monitor?: NetworkMonitorRecord
}) {
	const hasOpened = useRef(false)
	if (!monitor && !hasOpened.current) {
		return null
	}
	hasOpened.current = true
	return (
		<Dialog open={open} onOpenChange={setOpen}>
			<MonitorDialogContent open={open} setOpen={setOpen} systemId={systemId} monitor={monitor} />
		</Dialog>
	)
}

/** Initial runner of the form: the monitor's, else the current system's page, else the hub. */
function initialRunsOn(monitor?: NetworkMonitorRecord, systemId?: string): RunsOn {
	if (monitor) return monitor.system ? "agent" : "hub"
	return systemId ? "agent" : "hub"
}

function MonitorDialogContent({
	open,
	setOpen,
	systemId,
	monitor,
	onOpenBulkAdd,
}: {
	open: boolean
	setOpen: (open: boolean) => void
	systemId?: string
	monitor?: NetworkMonitorRecord
	onOpenBulkAdd?: (selectedSystemIds: Set<string>, runsOn: RunsOn) => void
}) {
	const [runsOn, setRunsOn] = useState<RunsOn>(() => initialRunsOn(monitor, systemId))
	const [name, setName] = useState(monitor?.name ?? "")
	const [protocol, setProtocol] = useState<MonitorProtocol>(monitor?.protocol ?? "icmp")
	const [target, setTarget] = useState(monitor?.target ?? "")
	const [port, setPort] = useState(monitor?.protocol === "tcp" && monitor.port ? String(monitor.port) : "")
	const [server, setServer] = useState(monitor?.protocol === "dns" ? (monitor.server ?? "") : "")
	const [monitorInterval, setMonitorInterval] = useState(String(monitor?.interval ?? defaultInterval))
	const [timeout, setTimeoutValue] = useState(monitor?.timeout ? String(monitor.timeout) : "")
	const [retries, setRetries] = useState(String(monitor?.retries ?? 1))
	const [retryInterval, setRetryInterval] = useState(monitor?.retryInterval ? String(monitor.retryInterval) : "")
	const [notify, setNotify] = useState(monitor?.notify ?? true)
	const [certExpiryDays, setCertExpiryDays] = useState(String(monitor?.certExpiryDays ?? 0))
	const [httpForm, setHttpForm] = useState<HttpFormState>(() => httpFormFromMonitor(monitor))
	const [loading, setLoading] = useState(false)
	const [selectedSystemId, setSelectedSystemId] = useState(monitor?.system ?? "")
	const [selectedSystemIds, setSelectedSystemIds] = useState<Set<string>>(new Set())
	const [createdPushMonitor, setCreatedPushMonitor] = useState<NetworkMonitorRecord | null>(null)
	const systems = useStore($systems)
	const allSystems = useStore($allSystemsById)
	const { toast } = useToast()
	const { t } = useLingui()
	const isEditing = !!monitor
	const isHub = runsOn === "hub"
	const isPush = protocol === "push"
	// Secret HTTP options are omitted from responses for users who can't see them.
	const secretsHidden = isEditing && !("httpSecrets" in monitor)

	// When the dialog is opened, initialize form fields with monitor values (if editing) or defaults (if adding).
	useEffect(() => {
		if (!open) {
			return
		}

		setRunsOn(initialRunsOn(monitor, systemId))
		setName(monitor?.name ?? "")
		setProtocol(monitor?.protocol ?? "icmp")
		setTarget(monitor?.target ?? "")
		setPort(monitor?.protocol === "tcp" && monitor.port ? String(monitor.port) : "")
		setServer(monitor?.protocol === "dns" ? (monitor.server ?? "") : "")
		setMonitorInterval(String(monitor?.interval ?? defaultInterval))
		setTimeoutValue(monitor?.timeout ? String(monitor.timeout) : "")
		setRetries(String(monitor?.retries ?? 1))
		setRetryInterval(monitor?.retryInterval ? String(monitor.retryInterval) : "")
		setNotify(monitor?.notify ?? true)
		setCertExpiryDays(String(monitor?.certExpiryDays ?? 0))
		setHttpForm(httpFormFromMonitor(monitor))
		setSelectedSystemId(monitor?.system ?? "")
		setSelectedSystemIds(new Set())
		setCreatedPushMonitor(null)
		setLoading(false)
	}, [open, monitor])

	const changeRunsOn = (value: RunsOn) => {
		setRunsOn(value)
		// push monitors only run on the hub
		if (value === "agent" && protocol === "push") {
			setProtocol("icmp")
		}
	}

	const agentSystemIds = isHub
		? []
		: systemId
			? [systemId]
			: isEditing
				? [selectedSystemId].filter(Boolean)
				: Array.from(selectedSystemIds)
	const usesNewAgentOptions =
		Number(timeout) > 0 ||
		Number(retryInterval) > 0 ||
		(protocol === "http" &&
			(hasCustomHttpOptions(httpForm) || (secretsHidden && !!monitor?.http && Object.keys(monitor.http).length > 0)))
	const outdatedAgents = usesNewAgentOptions
		? agentSystemIds.filter((id) => allSystems[id] && needsAgentUpdateForMonitorOptions(allSystems[id].info?.v))
		: []
	const showCertExpiry = protocol === "http" && isHttpsTarget(target)

	async function handleSubmit(e: React.FormEvent) {
		e.preventDefault()
		setLoading(true)

		const targetSystems = isHub ? [""] : agentSystemIds
		const remainingSystemIds = new Set(targetSystems)
		try {
			if (!targetSystems.length || (!isHub && !targetSystems[0])) {
				throw new Error(t`Select at least one system.`)
			}
			if (isHub && Number(monitorInterval) < hubMinInterval) {
				throw new Error(t`Hub monitors must use an interval of at least ${hubMinInterval} seconds.`)
			}
			const basePayload = buildMonitorPayload(
				{
					system: targetSystems[0],
					target,
					protocol,
					port: protocol === "tcp" ? Number(port) : 0,
					server: protocol === "dns" ? server.trim() : "",
					interval: monitorInterval,
				},
				monitor ? monitor.enabled : true
			)
			const payload: Record<string, unknown> = {
				...basePayload,
				name: name.trim(),
				timeout: isPush ? 0 : Number(timeout) || 0,
				retries: Number(retries) || 0,
				retryInterval: isPush ? 0 : Number(retryInterval) || 0,
				notify,
				certExpiryDays: protocol === "http" && isHttpsTarget(basePayload.target) ? Number(certExpiryDays) || 0 : 0,
			}
			if (protocol === "http") {
				const { http, httpSecrets } = httpPayloadFromForm(httpForm)
				payload.http = http
				// don't wipe secrets this user can't see
				if (!secretsHidden) payload.httpSecrets = httpSecrets
			} else {
				payload.http = null
				if (!secretsHidden) payload.httpSecrets = null
			}
			if (isHub) {
				// hub monitors need owners; keep existing owners when editing a hub monitor
				if (!monitor || monitor.system || !monitor.users?.length) {
					const userId = pb.authStore.record?.id
					payload.users = userId ? [userId] : []
				}
			}

			if (monitor) {
				await pb.collection("network_monitors").update(monitor.id, payload)
				setOpen(false)
				return
			}
			let createdPush: NetworkMonitorRecord | null = null
			for (const system of targetSystems) {
				const record = await pb.collection<NetworkMonitorRecord>("network_monitors").create({ ...payload, system })
				remainingSystemIds.delete(system)
				if (record.protocol === "push") createdPush = record
			}
			if (createdPush) {
				// keep the dialog open to show the push URL
				setCreatedPushMonitor(createdPush)
			} else {
				setOpen(false)
			}
		} catch (err: unknown) {
			if (!monitor && !isHub && !systemId) {
				// Retain only unfinished systems so retrying cannot duplicate successful creates.
				setSelectedSystemIds(remainingSystemIds)
			}
			toast({ variant: "destructive", title: t`Error`, description: getErrorMessage(err) })
		} finally {
			setLoading(false)
		}
	}

	if (createdPushMonitor) {
		return (
			<DialogContent className="max-w-lg">
				<DialogHeader>
					<DialogTitle>
						<Trans>Monitor created</Trans>
					</DialogTitle>
					<DialogDescription>
						<Trans>Send heartbeats to this URL from your service, cron job or script.</Trans>
					</DialogDescription>
				</DialogHeader>
				<MonitorPushUrl monitorId={createdPushMonitor.id} pushToken={createdPushMonitor.pushToken} />
				<DialogFooter>
					<Button type="button" onClick={() => setOpen(false)}>
						<Trans>Done</Trans>
					</Button>
				</DialogFooter>
			</DialogContent>
		)
	}

	const systemName = systemId ? allSystems[systemId]?.name : ""

	return (
		<DialogContent className="max-w-lg max-h-[calc(100dvh-2rem)] overflow-y-auto">
			<DialogHeader>
				<DialogTitle>
					{isEditing ? <Trans>Edit {{ foo: t`Monitor` }}</Trans> : <Trans>Add {{ foo: t`Monitor` }}</Trans>}
				</DialogTitle>
				<DialogDescription>
					<Trans>Check the availability and response time of a service from the hub or an agent.</Trans>
				</DialogDescription>
			</DialogHeader>
			<form onSubmit={handleSubmit} className="grid gap-4 tabular-nums">
				<div className="grid gap-2">
					<Label htmlFor="monitor-runs-on">
						<Trans>Runs on</Trans>
					</Label>
					<Select value={runsOn} onValueChange={(value) => changeRunsOn(value as RunsOn)}>
						<SelectTrigger id="monitor-runs-on">
							<SelectValue />
						</SelectTrigger>
						<SelectContent>
							<SelectItem value="hub">
								<Trans>Hub</Trans>
							</SelectItem>
							<SelectItem value="agent">{systemId ? systemName || t`This system` : t`Agent`}</SelectItem>
						</SelectContent>
					</Select>
					{isHub && !isEditing && (
						<p className="text-xs text-muted-foreground">
							<Trans>The hub checks the target itself. Only you can see this monitor.</Trans>
						</p>
					)}
				</div>
				{!isHub && !systemId && !isEditing && (
					<div className="grid gap-2">
						<Label htmlFor="monitor-systems">
							<Trans>Systems</Trans>
						</Label>
						<SystemMultiSelect
							id="monitor-systems"
							selectedSystemIds={selectedSystemIds}
							onChange={setSelectedSystemIds}
							disabled={loading}
						/>
					</div>
				)}
				{!isHub && !systemId && isEditing && (
					<div className="grid gap-2">
						<Label htmlFor="monitor-system">
							<Trans>System</Trans>
						</Label>
						<Select value={selectedSystemId} onValueChange={setSelectedSystemId} required>
							<SelectTrigger id="monitor-system">
								<SelectValue placeholder={t`Select a system`} />
							</SelectTrigger>
							<SelectContent>
								{systems
									.filter((sys) => sys.id === monitor?.system || supportsNetworkMonitors(sys))
									.map((sys) => (
										<SelectItem key={sys.id} value={sys.id}>
											{sys.name}
										</SelectItem>
									))}
							</SelectContent>
						</Select>
					</div>
				)}
				<div className="grid grid-cols-2 gap-3">
					<div className={isPush || protocol !== "tcp" ? "col-span-2 grid gap-2" : "grid gap-2"}>
						<Label htmlFor="monitor-protocol">
							<Trans>Protocol</Trans>
						</Label>
						<Select value={protocol} onValueChange={(value) => setProtocol(value as MonitorProtocol)}>
							<SelectTrigger id="monitor-protocol">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								<SelectItem value="icmp">ICMP</SelectItem>
								<SelectItem value="tcp">TCP</SelectItem>
								<SelectItem value="http">HTTP</SelectItem>
								<SelectItem value="dns">DNS</SelectItem>
								{isHub && <SelectItem value="push">Push</SelectItem>}
							</SelectContent>
						</Select>
					</div>
					{protocol === "tcp" && (
						<div className="grid gap-2">
							<Label htmlFor="monitor-port">
								<Trans>Port</Trans>
							</Label>
							<Input
								id="monitor-port"
								type="number"
								value={port}
								onChange={(e) => setPort(e.target.value)}
								placeholder="443"
								min={1}
								max={65535}
							/>
						</div>
					)}
				</div>
				{isPush ? (
					<p className="-mt-2 text-xs text-muted-foreground">
						<Trans>Push monitors wait for heartbeats sent to a unique URL and go down when none arrives in time.</Trans>
					</p>
				) : (
					<div className="grid gap-2">
						<Label htmlFor="monitor-target">
							<Trans>Target</Trans>
						</Label>
						<Input
							id="monitor-target"
							value={target}
							onChange={(e) => setTarget(e.target.value)}
							placeholder={
								protocol === "http" ? "http://localhost:8090" : protocol === "dns" ? "example.com" : "1.1.1.1"
							}
							required
						/>
					</div>
				)}
				{protocol === "dns" && (
					<div className="grid gap-2">
						<Label htmlFor="monitor-dns-server">
							<Trans>DNS Server</Trans>
						</Label>
						<Input
							id="monitor-dns-server"
							value={server}
							onChange={(e) => setServer(e.target.value)}
							placeholder="1.1.1.1"
						/>
						<p className="text-xs text-muted-foreground">
							<Trans>Optional. Defaults to the system resolver.</Trans>
						</p>
					</div>
				)}
				<div className="grid gap-2">
					<Label htmlFor="monitor-name">
						<Trans>Name</Trans>
					</Label>
					<Input
						id="monitor-name"
						value={name}
						onChange={(e) => setName(e.target.value)}
						placeholder={isPush ? t`Push monitor` : target.trim() || t`Optional`}
						maxLength={100}
					/>
				</div>
				<div className="grid grid-cols-2 gap-3">
					<div className="grid gap-2">
						<Label htmlFor="monitor-interval">
							{isPush ? <Trans>Heartbeat interval (s)</Trans> : <Trans>Interval (seconds)</Trans>}
						</Label>
						<Input
							id="monitor-interval"
							type="number"
							value={monitorInterval}
							onChange={(e) => setMonitorInterval(e.target.value)}
							min={isHub ? hubMinInterval : 1}
							max={3600}
							required
						/>
					</div>
					{!isPush && (
						<div className="grid gap-2">
							<Label htmlFor="monitor-timeout">
								<Trans>Timeout (seconds)</Trans>
							</Label>
							<Input
								id="monitor-timeout"
								type="number"
								value={timeout}
								onChange={(e) => setTimeoutValue(e.target.value)}
								placeholder={t`Default`}
								min={0}
								max={60}
							/>
						</div>
					)}
					<div className="grid gap-2">
						<Label htmlFor="monitor-retries">
							<Trans>Retries</Trans>
						</Label>
						<Input
							id="monitor-retries"
							type="number"
							value={retries}
							onChange={(e) => setRetries(e.target.value)}
							min={0}
							max={10}
						/>
					</div>
					{!isPush && (
						<div className="grid gap-2">
							<Label htmlFor="monitor-retry-interval">
								<Trans>Retry interval (s)</Trans>
							</Label>
							<Input
								id="monitor-retry-interval"
								type="number"
								value={retryInterval}
								onChange={(e) => setRetryInterval(e.target.value)}
								placeholder={t`Same as interval`}
								min={0}
								max={3600}
							/>
						</div>
					)}
				</div>
				<p className="-mt-2 text-xs text-muted-foreground">
					<Trans>Failed checks are retried this many times before the monitor is marked down.</Trans>
				</p>
				{showCertExpiry && (
					<div className="grid gap-2">
						<Label htmlFor="monitor-cert-days">
							<Trans>Certificate expiry notification (days)</Trans>
						</Label>
						<Input
							id="monitor-cert-days"
							type="number"
							value={certExpiryDays}
							onChange={(e) => setCertExpiryDays(e.target.value)}
							min={0}
							max={365}
						/>
						<p className="text-xs text-muted-foreground">
							<Trans>Notify when the certificate expires within this many days. 0 disables it.</Trans>
						</p>
					</div>
				)}
				{protocol === "http" && (
					<MonitorHttpOptions
						value={httpForm}
						onChange={setHttpForm}
						secretsHidden={secretsHidden}
						disabled={loading}
					/>
				)}
				<SwitchField
					id="monitor-notify"
					checked={notify}
					onCheckedChange={setNotify}
					label={<Trans>Notifications</Trans>}
					description={<Trans>Send notifications when this monitor goes down or recovers.</Trans>}
				/>
				{isEditing && isPush && monitor.protocol === "push" && (
					<MonitorPushUrl monitorId={monitor.id} pushToken={monitor.pushToken} />
				)}
				{outdatedAgents.length > 0 && (
					<div className="flex gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 p-3 text-sm text-amber-700 dark:text-amber-400">
						<TriangleAlertIcon className="size-4 shrink-0 mt-0.5" />
						<div>
							<p className="font-medium">
								<Trans>Agent update required for these options</Trans>
							</p>
							<p className="text-xs opacity-90">
								<Trans>
									HTTP options, timeouts and retry intervals need agent version 0.21.0 or newer:{" "}
									{outdatedAgents.map((id) => allSystems[id]?.name).join(", ")}
								</Trans>
							</p>
						</div>
					</div>
				)}
				<DialogFooter>
					{!isEditing && onOpenBulkAdd && (
						<Button
							type="button"
							variant="outline"
							onClick={() => onOpenBulkAdd(selectedSystemIds, runsOn)}
							disabled={loading}
							className="me-auto"
						>
							<ListIcon className="size-4 me-2" />
							<Trans>Bulk Add</Trans>
						</Button>
					)}
					<Button
						type="submit"
						disabled={loading || (!isHub && !systemId && (isEditing ? !selectedSystemId : !selectedSystemIds.size))}
					>
						{isEditing ? <Trans>Save {{ foo: t`Monitor` }}</Trans> : <Trans>Add {{ foo: t`Monitor` }}</Trans>}
					</Button>
				</DialogFooter>
			</form>
		</DialogContent>
	)
}
