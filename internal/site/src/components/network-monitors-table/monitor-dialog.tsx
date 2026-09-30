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
import { AlertRoutingFields, useNotificationChannels } from "@/components/notification-channels"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
	Select,
	SelectContent,
	SelectGroup,
	SelectItem,
	SelectLabel,
	SelectTrigger,
	SelectValue,
} from "@/components/ui/select"
import { useToast } from "@/components/ui/use-toast"
import { $allSystemsById, $systems } from "@/lib/stores"
import { supportsNetworkMonitors } from "@/lib/utils"
import { needsAgentUpdateForMonitorOptions } from "@/lib/network-monitor-utils"
import {
	defaultMonitorPort,
	isAgentOnlyProtocol,
	isCheckProtocol,
	monitorProtocolLabels,
	usesMonitorPort,
} from "@/lib/monitor-protocols"
import {
	defaultQuorum,
	getMonitorLocations,
	HUB_LOCATION,
	isMultiLocation,
	locationSystemIds,
	MAX_MONITOR_LOCATIONS,
	orderLocations,
	primaryLocationSystem,
} from "@/lib/monitor-locations"
import type { AlertSeverity, NetworkMonitorRecord } from "@/types"
import {
	buildMonitorPayload,
	type CheckFormState,
	checkFormFromMonitor,
	checkPayloadFromForm,
	defaultInterval,
	getErrorMessage,
	type HttpFormState,
	httpFormFromMonitor,
	httpPayloadFromForm,
	hubMinInterval,
	hasCustomCheckOptions,
	maxLossThreshold,
	type MonitorProtocol,
	monitorReportsCert,
	parseMonitorThreshold,
	usesCheckCredentials,
} from "./monitor-form-utils"
import { MonitorCheckDescription, MonitorCheckOptions } from "./monitor-check-options"
import { hasCustomHttpOptions, MonitorHttpOptions, SwitchField } from "./monitor-http-options"
import { MonitorPushUrl } from "./monitor-push-url"
import { isManagedField, managedContainer } from "@/lib/docker-discovery"
import { MonitorBulkAddSheet } from "./monitor-bulk-add-sheet"
import { SystemMultiSelect } from "./system-multi-select"
import { MonitorDependencySelect } from "./monitor-dependency-select"
import { UptimeKumaImportDialog } from "./uptime-kuma-import-dialog"

/** Where a monitor runs: the hub, one agent (or one monitor per selected agent), or several locations at once. */
type RunsOn = "hub" | "agent" | "locations"

/** Protocol select groups; push and docker are added depending on where the monitor runs. */
const protocolGroups: { id: string; protocols: MonitorProtocol[] }[] = [
	{ id: "network", protocols: ["icmp", "tcp", "dns", "ssh"] },
	{ id: "web", protocols: ["http", "grpc"] },
	{ id: "databases", protocols: ["postgres", "mysql", "redis"] },
	{ id: "mail", protocols: ["smtp", "imap"] },
	{ id: "games", protocols: ["minecraft", "a2s"] },
]

/** Port input value of a monitor; empty for protocols without a port. */
function portInput(monitor?: NetworkMonitorRecord) {
	return monitor && usesMonitorPort(monitor.protocol) && monitor.port ? String(monitor.port) : ""
}

/** Target placeholder of a protocol; docker uses the translated containerHint. */
function targetPlaceholder(protocol: MonitorProtocol, containerHint: string) {
	switch (protocol) {
		case "http":
			return "http://localhost:8090"
		case "dns":
			return "example.com"
		case "docker":
			return containerHint
		case "icmp":
		case "tcp":
			return "1.1.1.1"
		default:
			return "localhost"
	}
}

export function AddMonitorDialog({ systemId, monitors }: { systemId?: string; monitors: NetworkMonitorRecord[] }) {
	const [open, setOpen] = useState(false)
	const [bulkOpen, setBulkOpen] = useState(false)
	const [importOpen, setImportOpen] = useState(false)
	const [bulkRunsOn, setBulkRunsOn] = useState<Exclude<RunsOn, "locations">>("agent")
	const [bulkSelectedSystemIds, setBulkSelectedSystemIds] = useState<Set<string>>(new Set())
	const { t } = useLingui()
	const systems = useStore($systems)
	const allSystems = useStore($allSystemsById)
	const hasEligibleSystems = systemId ? true : systems.some(supportsNetworkMonitors)

	const openBulkAdd = (selectedSystemIds?: Set<string>, runsOn?: RunsOn) => {
		if (!systemId && selectedSystemIds) {
			setBulkSelectedSystemIds(new Set(selectedSystemIds))
		}
		setBulkRunsOn(runsOn === "hub" || (!runsOn && !hasEligibleSystems) ? "hub" : "agent")
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

/** Input value of a monitor alert threshold; empty when off. */
function thresholdInput(value?: number) {
	return value ? String(value) : ""
}

/** Initial runner of the form: the monitor's, else the current system's page, else the hub. */
function initialRunsOn(monitor?: NetworkMonitorRecord, systemId?: string): RunsOn {
	if (monitor) {
		if (isMultiLocation(monitor)) return "locations"
		return monitor.system ? "agent" : "hub"
	}
	return systemId ? "agent" : "hub"
}

/** Initial locations of the multiple locations mode. */
function initialLocations(monitor?: NetworkMonitorRecord, systemId?: string) {
	if (monitor) return new Set(getMonitorLocations(monitor))
	return new Set(systemId ? [systemId] : [])
}

/** Quorum input value: empty uses the default majority. */
function quorumInput(monitor?: NetworkMonitorRecord) {
	return monitor && isMultiLocation(monitor) && monitor.quorum ? String(monitor.quorum) : ""
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
	const [port, setPort] = useState(() => portInput(monitor))
	const [server, setServer] = useState(monitor?.protocol === "dns" ? (monitor.server ?? "") : "")
	const [monitorInterval, setMonitorInterval] = useState(String(monitor?.interval ?? defaultInterval))
	const [timeout, setTimeoutValue] = useState(monitor?.timeout ? String(monitor.timeout) : "")
	const [retries, setRetries] = useState(String(monitor?.retries ?? 1))
	const [retryInterval, setRetryInterval] = useState(monitor?.retryInterval ? String(monitor.retryInterval) : "")
	const [notify, setNotify] = useState(monitor?.notify ?? true)
	const [severity, setSeverity] = useState<AlertSeverity | "">(monitor?.severity ?? "")
	const [channelIds, setChannelIds] = useState<string[]>(monitor?.channels ?? [])
	const { channels: notificationChannels } = useNotificationChannels()
	const [dependsOn, setDependsOn] = useState<string[]>(monitor?.dependsOn ?? [])
	const [certExpiryDays, setCertExpiryDays] = useState(String(monitor?.certExpiryDays ?? 0))
	const [lossThreshold, setLossThreshold] = useState(thresholdInput(monitor?.lossThreshold))
	const [latencyThreshold, setLatencyThreshold] = useState(thresholdInput(monitor?.latencyThreshold))
	const [httpForm, setHttpForm] = useState<HttpFormState>(() => httpFormFromMonitor(monitor))
	const [checkForm, setCheckForm] = useState<CheckFormState>(() => checkFormFromMonitor(monitor))
	const [loading, setLoading] = useState(false)
	const [selectedSystemId, setSelectedSystemId] = useState(monitor?.system ?? "")
	const [selectedSystemIds, setSelectedSystemIds] = useState<Set<string>>(new Set())
	const [locations, setLocations] = useState<Set<string>>(() => initialLocations(monitor, systemId))
	const [quorum, setQuorum] = useState(() => quorumInput(monitor))
	const [createdPushMonitor, setCreatedPushMonitor] = useState<NetworkMonitorRecord | null>(null)
	const systems = useStore($systems)
	const allSystems = useStore($allSystemsById)
	const { toast } = useToast()
	const { t } = useLingui()
	const isEditing = !!monitor
	const isHub = runsOn === "hub"
	const isLocations = runsOn === "locations"
	// the hub checks the monitor itself, alone or as one of several locations
	const hubChecks = isHub || (isLocations && locations.has(HUB_LOCATION))
	const isPush = protocol === "push"
	const orderedLocations = orderLocations(locations)
	const defaultLocationQuorum = defaultQuorum(orderedLocations.length)
	// Secret HTTP options are omitted from responses for users who can't see them.
	const secretsHidden = isEditing && !("httpSecrets" in monitor)
	// Fields set by Docker labels are read-only (the hub keeps their stored values).
	const locked = (field: string) => isManagedField(monitor, field)
	const dockerContainer = managedContainer(monitor)

	// When the dialog is opened, initialize form fields with monitor values (if editing) or defaults (if adding).
	useEffect(() => {
		if (!open) {
			return
		}

		setRunsOn(initialRunsOn(monitor, systemId))
		setName(monitor?.name ?? "")
		setProtocol(monitor?.protocol ?? "icmp")
		setTarget(monitor?.target ?? "")
		setPort(portInput(monitor))
		setServer(monitor?.protocol === "dns" ? (monitor.server ?? "") : "")
		setMonitorInterval(String(monitor?.interval ?? defaultInterval))
		setTimeoutValue(monitor?.timeout ? String(monitor.timeout) : "")
		setRetries(String(monitor?.retries ?? 1))
		setRetryInterval(monitor?.retryInterval ? String(monitor.retryInterval) : "")
		setNotify(monitor?.notify ?? true)
		setSeverity(monitor?.severity ?? "")
		setChannelIds(monitor?.channels ?? [])
		setDependsOn(monitor?.dependsOn ?? [])
		setCertExpiryDays(String(monitor?.certExpiryDays ?? 0))
		setLossThreshold(thresholdInput(monitor?.lossThreshold))
		setLatencyThreshold(thresholdInput(monitor?.latencyThreshold))
		setHttpForm(httpFormFromMonitor(monitor))
		setCheckForm(checkFormFromMonitor(monitor))
		setSelectedSystemId(monitor?.system ?? "")
		setSelectedSystemIds(new Set())
		setLocations(initialLocations(monitor, systemId))
		setQuorum(quorumInput(monitor))
		setCreatedPushMonitor(null)
		setLoading(false)
	}, [open, monitor])

	const changeRunsOn = (value: RunsOn) => {
		setRunsOn(value)
		// push monitors only run on the hub alone, docker monitors only on agents
		if (
			(value !== "hub" && protocol === "push") ||
			(value === "hub" && isAgentOnlyProtocol(protocol)) ||
			(value === "locations" && locations.has(HUB_LOCATION) && isAgentOnlyProtocol(protocol))
		) {
			setProtocol("icmp")
		}
	}

	const changeLocations = (value: Set<string>) => {
		setLocations(value)
		if (value.has(HUB_LOCATION) && isAgentOnlyProtocol(protocol)) {
			setProtocol("icmp")
		}
	}

	/** Default port of the protocol with the current TLS options. */
	const defaultPortFor = (value: MonitorProtocol, form = checkForm) => {
		if (value === "tcp") return 0
		const { check } = checkPayloadFromForm(value, form)
		return defaultMonitorPort(value, check)
	}

	/** Changes the protocol, replacing a port that is empty or the previous protocol's default. */
	const changeProtocol = (value: MonitorProtocol) => {
		const previousDefault = defaultPortFor(protocol)
		if (!port || Number(port) === previousDefault) {
			const next = defaultPortFor(value)
			setPort(next ? String(next) : "")
		}
		setProtocol(value)
	}

	/** Updates check options, following default smtp and imap ports when the TLS mode changes. */
	const changeCheckForm = (form: CheckFormState) => {
		const previousDefault = defaultPortFor(protocol)
		if (!port || Number(port) === previousDefault) {
			const next = defaultPortFor(protocol, form)
			setPort(next ? String(next) : "")
		}
		setCheckForm(form)
	}

	const agentSystemIds = isHub
		? []
		: isLocations
			? locationSystemIds(orderedLocations)
			: systemId
				? [systemId]
				: isEditing
					? [selectedSystemId].filter(Boolean)
					: Array.from(selectedSystemIds)
	const usesNewAgentOptions =
		Number(timeout) > 0 ||
		Number(retryInterval) > 0 ||
		isCheckProtocol(protocol) ||
		hasCustomCheckOptions(protocol, checkForm) ||
		(protocol === "http" &&
			(hasCustomHttpOptions(httpForm) || (secretsHidden && !!monitor?.http && Object.keys(monitor.http).length > 0)))
	const outdatedAgents = usesNewAgentOptions
		? agentSystemIds.filter((id) => allSystems[id] && needsAgentUpdateForMonitorOptions(allSystems[id].info?.v))
		: []
	const showCertExpiry = monitorReportsCert(protocol, target, checkForm)
	const hasPort = usesMonitorPort(protocol)
	const protocolGroupLabels: Record<string, string> = {
		network: t`Network`,
		web: t`Web`,
		databases: t`Databases`,
		mail: t`Mail`,
		games: t`Games`,
	}

	async function handleSubmit(e: React.FormEvent) {
		e.preventDefault()
		setLoading(true)

		const targetSystems = isHub ? [""] : isLocations ? [primaryLocationSystem(orderedLocations)] : agentSystemIds
		const remainingSystemIds = new Set(targetSystems)
		try {
			if (isLocations) {
				if (!orderedLocations.length) {
					throw new Error(t`Select at least one location.`)
				}
				if (orderedLocations.length > MAX_MONITOR_LOCATIONS) {
					throw new Error(t`A monitor can run from at most ${MAX_MONITOR_LOCATIONS} locations.`)
				}
			} else if (!targetSystems.length || (!isHub && !targetSystems[0])) {
				throw new Error(t`Select at least one system.`)
			}
			const quorumValue = isLocations && orderedLocations.length > 1 && quorum.trim() ? Number(quorum) : 0
			if (!Number.isInteger(quorumValue) || quorumValue < 0 || quorumValue > orderedLocations.length) {
				throw new Error(t`Quorum must be between 1 and ${orderedLocations.length}.`)
			}
			if (hubChecks && Number(monitorInterval) < hubMinInterval) {
				throw new Error(t`Hub monitors must use an interval of at least ${hubMinInterval} seconds.`)
			}
			// push monitors have no loss or response time to alert on
			const lossValue = isPush ? 0 : parseMonitorThreshold(lossThreshold, { max: maxLossThreshold })
			if (lossValue === null) {
				throw new Error(t`Packet loss threshold must be between 0 and ${maxLossThreshold}.`)
			}
			const latencyValue = isPush ? 0 : parseMonitorThreshold(latencyThreshold, { max: 600000, integer: true })
			if (latencyValue === null) {
				throw new Error(t`Response time threshold must be a whole number of milliseconds.`)
			}
			const checkPayload = checkPayloadFromForm(protocol, checkForm)
			const basePayload = buildMonitorPayload(
				{
					system: targetSystems[0],
					target,
					protocol,
					port: hasPort ? Number(port) : 0,
					server: protocol === "dns" ? server.trim() : "",
					interval: monitorInterval,
					check: checkPayload.check,
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
				severity,
				channels: channelIds,
				dependsOn,
				certExpiryDays: monitorReportsCert(protocol, basePayload.target, checkForm) ? Number(certExpiryDays) || 0 : 0,
				lossThreshold: lossValue,
				latencyThreshold: latencyValue,
			}
			payload.check = checkPayload.check
			if (protocol === "http") {
				const { http, httpSecrets } = httpPayloadFromForm(httpForm)
				payload.http = http
				// don't wipe secrets this user can't see
				if (!secretsHidden) payload.httpSecrets = httpSecrets
			} else {
				payload.http = null
				if (!secretsHidden) {
					payload.httpSecrets = usesCheckCredentials(protocol) ? checkPayload.secrets : null
				}
			}
			// 0 lets the hub use the default majority
			payload.quorum = quorumValue
			if (isLocations) {
				// the create rule checks the primary system; the hub derives it from the locations too
				payload.locations = orderedLocations
				payload.system = targetSystems[0]
			} else {
				payload.locations = [targetSystems[0] || HUB_LOCATION]
			}
			if (hubChecks) {
				// hub monitors need owners; keep existing owners when editing a hub monitor
				if (!monitor || !getMonitorLocations(monitor).includes(HUB_LOCATION) || !monitor.users?.length) {
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
				const record = await pb.collection<NetworkMonitorRecord>("network_monitors").create({
					...payload,
					system,
					locations: isLocations ? orderedLocations : [system || HUB_LOCATION],
				})
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
			if (!monitor && runsOn === "agent" && !systemId) {
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
				{dockerContainer && (
					<p className="rounded-md border bg-muted/50 px-3 py-2 text-xs text-muted-foreground">
						<Trans>
							Managed by Docker labels on <span className="font-mono">{dockerContainer}</span>. Fields set by labels are
							read-only.
						</Trans>
					</p>
				)}
				<div className="grid gap-2">
					<Label htmlFor="monitor-runs-on">
						<Trans>Runs on</Trans>
					</Label>
					<Select
						value={runsOn}
						onValueChange={(value) => changeRunsOn(value as RunsOn)}
						disabled={locked("locations")}
					>
						<SelectTrigger id="monitor-runs-on">
							<SelectValue />
						</SelectTrigger>
						<SelectContent>
							<SelectItem value="hub">
								<Trans>Hub</Trans>
							</SelectItem>
							<SelectItem value="agent">{systemId ? systemName || t`This system` : t`Agent`}</SelectItem>
							<SelectItem value="locations">
								<Trans>Multiple locations</Trans>
							</SelectItem>
						</SelectContent>
					</Select>
					{isHub && !isEditing && (
						<p className="text-xs text-muted-foreground">
							<Trans>The hub checks the target itself. Only you can see this monitor.</Trans>
						</p>
					)}
					{isLocations && (
						<p className="text-xs text-muted-foreground">
							<Trans>One monitor checked from the hub and agents at once. Its status combines all locations.</Trans>
						</p>
					)}
				</div>
				{isLocations && (
					<div className="grid gap-2">
						<Label htmlFor="monitor-locations">
							<Trans>Locations</Trans>
						</Label>
						<SystemMultiSelect
							id="monitor-locations"
							selectedSystemIds={locations}
							onChange={changeLocations}
							disabled={loading}
							includeHub={!isPush}
							keepIds={monitor ? getMonitorLocations(monitor) : undefined}
							placeholder={t`Select locations`}
						/>
					</div>
				)}
				{isLocations && orderedLocations.length > 1 && (
					<div className="grid gap-2">
						<Label htmlFor="monitor-quorum">
							<Trans>Quorum</Trans>
						</Label>
						<div className="flex items-center gap-2 text-sm">
							<Trans>
								<span className="shrink-0">Down when at least</span>
								<Input
									id="monitor-quorum"
									type="number"
									className="w-20"
									value={quorum}
									onChange={(e) => setQuorum(e.target.value)}
									placeholder={String(defaultLocationQuorum)}
									min={1}
									max={orderedLocations.length}
								/>
								<span>of {orderedLocations.length} locations fail</span>
							</Trans>
						</div>
						<p className="text-xs text-muted-foreground">
							<Trans>
								Fewer failing locations mark the monitor pending. Unreachable agents are left out; with fewer reporting
								locations than the quorum, the status is unknown.
							</Trans>
						</p>
					</div>
				)}
				{runsOn === "agent" && !systemId && !isEditing && (
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
				{runsOn === "agent" && !systemId && isEditing && (
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
					<div className={hasPort ? "grid gap-2" : "col-span-2 grid gap-2"}>
						<Label htmlFor="monitor-protocol">
							<Trans>Protocol</Trans>
						</Label>
						<Select
							value={protocol}
							onValueChange={(value) => changeProtocol(value as MonitorProtocol)}
							disabled={locked("protocol")}
						>
							<SelectTrigger id="monitor-protocol">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								{protocolGroups.map((group) => (
									<SelectGroup key={group.id}>
										<SelectLabel>{protocolGroupLabels[group.id]}</SelectLabel>
										{group.protocols.map((value) => (
											<SelectItem key={value} value={value}>
												{monitorProtocolLabels[value]}
											</SelectItem>
										))}
									</SelectGroup>
								))}
								{isHub ? (
									<SelectGroup>
										<SelectLabel>
											<Trans>Heartbeat</Trans>
										</SelectLabel>
										<SelectItem value="push">{monitorProtocolLabels.push}</SelectItem>
									</SelectGroup>
								) : (
									!hubChecks && (
										<SelectGroup>
											<SelectLabel>
												<Trans>Containers</Trans>
											</SelectLabel>
											<SelectItem value="docker">{monitorProtocolLabels.docker}</SelectItem>
										</SelectGroup>
									)
								)}
							</SelectContent>
						</Select>
					</div>
					{hasPort && (
						<div className="grid gap-2">
							<Label htmlFor="monitor-port">
								<Trans>Port</Trans>
							</Label>
							<Input
								id="monitor-port"
								type="number"
								value={port}
								onChange={(e) => setPort(e.target.value)}
								placeholder={protocol === "tcp" ? "443" : String(defaultPortFor(protocol) || "")}
								required={protocol === "grpc"}
								disabled={locked("port")}
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
							placeholder={targetPlaceholder(protocol, t`Container name or ID`)}
							disabled={locked("target")}
							required
						/>
					</div>
				)}
				{!isPush && <MonitorCheckDescription protocol={protocol} />}
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
						disabled={locked("name")}
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
							min={hubChecks ? hubMinInterval : 1}
							max={3600}
							disabled={locked("interval")}
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
								disabled={locked("timeout")}
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
							disabled={locked("retries")}
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
				<MonitorCheckOptions
					key={protocol}
					protocol={protocol}
					value={checkForm}
					onChange={changeCheckForm}
					secretsHidden={secretsHidden}
					disabled={loading}
				/>
				{protocol === "http" && (
					<MonitorHttpOptions
						value={httpForm}
						onChange={setHttpForm}
						secretsHidden={secretsHidden}
						disabled={loading}
						locked={locked}
					/>
				)}
				<SwitchField
					id="monitor-notify"
					checked={notify}
					onCheckedChange={setNotify}
					disabled={locked("notify")}
					label={<Trans>Notifications</Trans>}
					description={<Trans>Send notifications when this monitor goes down or recovers.</Trans>}
				/>
				<AlertRoutingFields
					idPrefix="monitor"
					severity={severity}
					onSeverityChange={setSeverity}
					channelIds={channelIds}
					onChannelsChange={setChannelIds}
					channels={notificationChannels}
					disabled={loading}
					defaultHint="critical"
				/>
				<div className="grid gap-2">
					<Label htmlFor="monitor-depends-on">
						<Trans>Depends on</Trans>
					</Label>
					<MonitorDependencySelect
						id="monitor-depends-on"
						value={dependsOn}
						onChange={setDependsOn}
						monitorId={monitor?.id}
						disabled={loading}
					/>
					<p className="text-xs text-muted-foreground">
						<Trans>
							While any of these monitors is down, alerts for this monitor are suppressed and it shows as unreachable.
						</Trans>
					</p>
				</div>
				{!isPush && (
					<div className="grid gap-2">
						<div className="grid sm:grid-cols-2 items-end gap-3">
							<div className="grid gap-2">
								<Label htmlFor="monitor-loss-threshold">
									<Trans>Alert when packet loss exceeds (%)</Trans>
								</Label>
								<Input
									id="monitor-loss-threshold"
									type="number"
									value={lossThreshold}
									onChange={(e) => setLossThreshold(e.target.value)}
									placeholder={t`Off`}
									min={0}
									max={maxLossThreshold}
									step="any"
								/>
							</div>
							<div className="grid gap-2">
								<Label htmlFor="monitor-latency-threshold">
									<Trans>Alert when average response time exceeds (ms)</Trans>
								</Label>
								<Input
									id="monitor-latency-threshold"
									type="number"
									value={latencyThreshold}
									onChange={(e) => setLatencyThreshold(e.target.value)}
									placeholder={t`Off`}
									min={0}
									max={600000}
									step={1}
								/>
							</div>
						</div>
						<p className="text-xs text-muted-foreground">
							<Trans>Measured over the last hour. Leave empty or 0 to disable.</Trans>
						</p>
					</div>
				)}
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
									This protocol, check options, HTTP options, timeouts and retry intervals need agent version 0.21.0 or
									newer: {outdatedAgents.map((id) => allSystems[id]?.name).join(", ")}
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
						disabled={
							loading ||
							(isLocations
								? !locations.size
								: runsOn === "agent" && !systemId && (isEditing ? !selectedSystemId : !selectedSystemIds.size))
						}
					>
						{isEditing ? <Trans>Save {{ foo: t`Monitor` }}</Trans> : <Trans>Add {{ foo: t`Monitor` }}</Trans>}
					</Button>
				</DialogFooter>
			</form>
		</DialogContent>
	)
}
