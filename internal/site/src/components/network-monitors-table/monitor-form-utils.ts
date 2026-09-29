import type { ClientResponseError } from "pocketbase"
import * as v from "valibot"
import {
	checkUsesTls,
	containerRefPattern,
	defaultMonitorPort,
	monitorProtocols,
	supportsStartTls,
	supportsTls,
	usesMonitorPort,
} from "@/lib/monitor-protocols"
import type {
	MonitorCheckOptions,
	MonitorDNSRecordType,
	MonitorHTTPOptions,
	MonitorHTTPSecrets,
	MonitorProtocol,
	NetworkMonitorRecord,
} from "@/types"

export type { MonitorProtocol }

export type MonitorValues = {
	system: string
	target: string
	protocol: MonitorProtocol
	port: number
	server: string
	interval: string
	/** TLS options select the default smtp and imap ports. */
	check?: Pick<MonitorCheckOptions, "tls" | "startTLS"> | null
}

type NormalizedMonitorValues = Omit<MonitorValues, "system" | "interval" | "check"> & {
	interval: number
}

type BulkMonitorLineSource = Pick<
	NetworkMonitorRecord,
	"target" | "protocol" | "port" | "interval" | "server" | "check"
>

export const defaultInterval = 30
/** Shortest interval of a hub monitor (the hub may enforce a longer one). */
export const hubMinInterval = 10

const MonitorProtocolSchema = v.picklist(monitorProtocols)

const MonitorIntervalSchema = v.pipe(v.string(), v.toNumber(), v.minValue(1), v.maxValue(3600))

// Both the single-monitor form and the bulk importer flow through this schema so
// defaults and HTTP target normalization stay in one place.
const NormalizedMonitorValuesSchema = v.pipe(
	v.object({
		target: v.pipe(v.string(), v.trim()),
		protocol: MonitorProtocolSchema,
		port: v.number(),
		server: v.pipe(v.string(), v.trim()),
		interval: MonitorIntervalSchema,
		check: v.optional(v.nullable(v.object({ tls: v.optional(v.boolean()), startTLS: v.optional(v.boolean()) }))),
	}),
	v.transform((input): NormalizedMonitorValues => {
		let { protocol, port } = input
		let httpTarget = input.target
		if (!usesMonitorPort(protocol)) {
			if (protocol === "http" && input.target) {
				httpTarget = normalizeHttpTarget(input.target, port)
			}
			port = 0
		} else if (!port) {
			port = protocol === "tcp" ? 443 : defaultMonitorPort(protocol, input.check)
		}
		return {
			// HTTP monitors may be entered as bare hostnames, so normalize them to a
			// scheme-bearing URL before the payload is sent to PocketBase.
			// Push monitors have no target.
			target: protocol === "push" ? "" : protocol === "http" ? httpTarget : input.target,
			protocol,
			port,
			// Only DNS monitors use a custom server; clear it for other protocols.
			server: protocol === "dns" ? input.server : "",
			interval: input.interval,
		}
	}),
	v.forward(
		v.check((input) => input.protocol === "push" || input.target !== "", "Target is required"),
		["target"]
	),
	v.forward(
		v.check((input) => {
			if (!usesMonitorPort(input.protocol)) {
				return input.port === 0
			}

			return Number.isInteger(input.port) && input.port >= 1 && input.port <= 65535
		}, "Port must be between 1 and 65535"),
		["port"]
	),
	v.forward(
		v.check(
			(input) => input.protocol !== "docker" || containerRefPattern.test(input.target),
			"Target must be a container name or ID"
		),
		["target"]
	)
)

// Bulk parsing only trims raw CSV fields. Inference, defaults, and protocol-
// specific validation still go through the shared normalization schema above.
const BulkMonitorSchema = v.object({
	target: v.pipe(v.string(), v.trim(), v.nonEmpty("target is required")),
	protocol: v.optional(v.pipe(v.string(), v.trim())),
	port: v.optional(v.pipe(v.string(), v.trim())),
	interval: v.optional(v.pipe(v.string(), v.trim())),
	server: v.optional(v.pipe(v.string(), v.trim())),
})

export function normalizeHttpTarget(target: string, port = 0) {
	const useExplicitPort = port > 0 && port !== 80 && port !== 443
	const hasOriginOnlyTarget = /^https?:\/\/[^/?#]+$/i.test(target)
	if (!/^https?:\/\//i.test(target)) {
		const scheme = port === 80 ? "http" : "https"
		return `${scheme}://${target}${useExplicitPort ? `:${port}` : ""}`
	}

	let parsedUrl: URL
	try {
		parsedUrl = new URL(target)
	} catch {
		return target
	}

	if (!parsedUrl.port && useExplicitPort) {
		parsedUrl.port = `${port}`
	}

	// avoid converting "http://localhost:8090" to "http://localhost:8090/" - keep the original formatting if the URL is just an origin
	if (hasOriginOnlyTarget && parsedUrl.pathname === "/" && !parsedUrl.search && !parsedUrl.hash) {
		return parsedUrl.origin
	}

	return parsedUrl.toString()
}

/** Whether an http target is (or normalizes to) an https URL. */
export function isHttpsTarget(target: string) {
	return !/^http:\/\//i.test(target.trim())
}

function trimTrailingEmptyFields(fields: string[]) {
	let lastValueIndex = fields.length - 1
	while (lastValueIndex > 0 && !fields[lastValueIndex]) {
		lastValueIndex--
	}
	return fields.slice(0, lastValueIndex + 1)
}

export function buildMonitorPayload(values: MonitorValues, enabled = true) {
	const normalizedValues = v.safeParse(NormalizedMonitorValuesSchema, values)
	if (!normalizedValues.success) {
		throw new Error(normalizedValues.issues[0]?.message || "Invalid monitor")
	}

	return {
		system: values.system,
		enabled,
		...normalizedValues.output,
	}
}

type MonitorIdentity = Pick<MonitorValues, "system" | "target" | "protocol" | "port" | "server">
export function getMonitorIdentityKey({ system, target, protocol, port, server }: MonitorIdentity) {
	return `${system}${target}${protocol}${port}${protocol === "dns" ? server : ""}`
}

/** Whether a bulk line is a push monitor, which can't be bulk added. */
export function isBulkPushLine(line: string) {
	return line.split(",")[1]?.trim().toLowerCase() === "push"
}

export function parseBulkMonitorLine(line: string, lineNumber: number, system: string) {
	const [rawTarget = "", rawProtocol = "", rawPort = "", rawInterval = "", rawServer = ""] = line.split(",")
	const parsed = v.safeParse(BulkMonitorSchema, {
		target: rawTarget,
		protocol: rawProtocol,
		port: rawPort,
		interval: rawInterval,
		server: rawServer,
	})
	if (!parsed.success) {
		throw new Error(`Line ${lineNumber}: ${parsed.issues[0]?.message || "invalid monitor entry"}`)
	}
	const protocol = (parsed.output.protocol?.toLowerCase() ||
		(/^https?:\/\//i.test(parsed.output.target) ? "http" : "icmp")) as MonitorProtocol
	if (protocol === "push") {
		throw new Error(`Line ${lineNumber}: push monitors can't be bulk added`)
	}

	return buildMonitorPayload({
		system,
		target: parsed.output.target,
		protocol,
		port: parsed.output.port ? Number(parsed.output.port) : 0,
		server: parsed.output.server || "",
		interval: parsed.output.interval || `${defaultInterval}`,
	})
}

export function formatBulkMonitorLine(monitor: BulkMonitorLineSource) {
	const defaultPort = monitor.protocol === "tcp" ? 443 : defaultMonitorPort(monitor.protocol, monitor.check)
	const port = !usesMonitorPort(monitor.protocol) || monitor.port === defaultPort ? "" : `${monitor.port}`
	const interval = monitor.interval === defaultInterval ? "" : `${monitor.interval}`
	const server = monitor.protocol !== "dns" ? "" : monitor.server
	return trimTrailingEmptyFields([monitor.target, monitor.protocol, port, interval, server]).join(",")
}

/** Largest packet loss threshold in percent; the hub requires it below 100. */
export const maxLossThreshold = 99.9

/**
 * Parses a monitor alert threshold input. Empty means 0 (off). Returns null when
 * the value is not a number in [0, max], or not a whole number when integer is set.
 */
export function parseMonitorThreshold(
	value: string,
	{ max, integer = false }: { max?: number; integer?: boolean } = {}
) {
	const trimmed = value.trim()
	if (!trimmed) return 0
	const num = Number(trimmed)
	if (!Number.isFinite(num) || num < 0 || (max !== undefined && num > max) || (integer && !Number.isInteger(num))) {
		return null
	}
	return num
}

/** Error message of a failed request, including PocketBase field validation errors. */
export function getErrorMessage(err: unknown) {
	const response = (err as ClientResponseError)?.response as
		| { message?: string; data?: Record<string, { message?: string }> }
		| undefined
	const fieldErrors = Object.entries(response?.data ?? {})
		.filter(([, value]) => value?.message)
		.map(([field, value]) => `${field}: ${value.message}`)
	if (fieldErrors.length) {
		return fieldErrors.join("\n")
	}
	return response?.message || (err as Error)?.message || String(err)
}

// ---------- HTTP options ----------

export type HttpMethod = NonNullable<MonitorHTTPOptions["method"]>
export const httpMethods: HttpMethod[] = ["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"]

export type HeaderRow = { id: number; name: string; value: string }

/** Editable state of the HTTP options form. */
export type HttpFormState = {
	method: HttpMethod
	acceptedCodes: string
	maxRedirects: number
	ignoreTLS: boolean
	keyword: string
	keywordInvert: boolean
	jsonPath: string
	jsonExpected: string
	headers: HeaderRow[]
	body: string
	basicUser: string
	basicPass: string
}

let headerRowId = 0
export function newHeaderRow(name = "", value = ""): HeaderRow {
	headerRowId++
	return { id: headerRowId, name, value }
}

export function httpFormFromMonitor(monitor?: Pick<NetworkMonitorRecord, "http" | "httpSecrets">): HttpFormState {
	const http = monitor?.http ?? {}
	const secrets = monitor?.httpSecrets ?? {}
	return {
		method: http.method || "GET",
		acceptedCodes: (http.acceptedCodes ?? []).join(", "),
		maxRedirects: http.maxRedirects ?? 0,
		ignoreTLS: !!http.ignoreTLS,
		keyword: http.keyword ?? "",
		keywordInvert: !!http.keywordInvert,
		jsonPath: http.jsonPath ?? "",
		jsonExpected: http.jsonExpected ?? "",
		headers: (secrets.headers ?? []).map(([name, value]) => newHeaderRow(name, value)),
		body: secrets.body ?? "",
		basicUser: secrets.basicUser ?? "",
		basicPass: secrets.basicPass ?? "",
	}
}

/** Splits the HTTP form into the non-secret and secret record fields; null when empty. */
export function httpPayloadFromForm(form: HttpFormState): {
	http: MonitorHTTPOptions | null
	httpSecrets: MonitorHTTPSecrets | null
} {
	const http: MonitorHTTPOptions = {}
	if (form.method !== "GET") http.method = form.method
	const acceptedCodes = form.acceptedCodes
		.split(",")
		.map((code) => code.trim())
		.filter(Boolean)
	if (acceptedCodes.length) http.acceptedCodes = acceptedCodes
	if (form.maxRedirects) http.maxRedirects = form.maxRedirects
	if (form.ignoreTLS) http.ignoreTLS = true
	if (form.keyword) {
		http.keyword = form.keyword
		if (form.keywordInvert) http.keywordInvert = true
	}
	if (form.jsonPath.trim()) {
		http.jsonPath = form.jsonPath.trim()
		http.jsonExpected = form.jsonExpected
	}

	const secrets: MonitorHTTPSecrets = {}
	const headers = form.headers
		.filter((header) => header.name.trim() || header.value)
		.map(({ name, value }): [string, string] => [name.trim(), value])
	if (headers.length) secrets.headers = headers
	// GET and HEAD requests don't send a body
	if (form.body && form.method !== "GET" && form.method !== "HEAD") secrets.body = form.body
	if (form.basicUser || form.basicPass) {
		secrets.basicUser = form.basicUser
		secrets.basicPass = form.basicPass
	}

	return {
		http: Object.keys(http).length ? http : null,
		httpSecrets: Object.keys(secrets).length ? secrets : null,
	}
}

// ---------- Check options ----------

export const dnsRecordTypes: MonitorDNSRecordType[] = ["A", "AAAA", "CNAME", "MX", "TXT", "NS", "SRV"]

/** TLS mode of a check: none, STARTTLS (smtp and imap) or implicit TLS. */
export type TlsMode = "none" | "starttls" | "tls"

/** Editable state of the check options form. */
export type CheckFormState = {
	/** Empty resolves A and AAAA records. */
	recordType: MonitorDNSRecordType | ""
	expected: string
	matchMode: "contains" | "equals"
	banner: string
	tlsMode: TlsMode
	ignoreTLS: boolean
	service: string
	username: string
	password: string
}

export function checkFormFromMonitor(
	monitor?: Pick<NetworkMonitorRecord, "check" | "httpSecrets"> | null
): CheckFormState {
	const check = monitor?.check ?? {}
	const secrets = monitor?.httpSecrets ?? {}
	return {
		recordType: check.recordType ?? "",
		expected: check.expected ?? "",
		matchMode: check.matchMode === "equals" ? "equals" : "contains",
		banner: check.banner ?? "",
		tlsMode: check.tls ? "tls" : check.startTLS ? "starttls" : "none",
		ignoreTLS: !!check.ignoreTLS,
		service: check.service ?? "",
		username: secrets.username ?? "",
		password: secrets.password ?? "",
	}
}

/** Whether the protocol stores credentials in httpSecrets. */
export function usesCheckCredentials(protocol: MonitorProtocol) {
	return protocol === "postgres" || protocol === "redis"
}

/**
 * Builds the check options and credentials the protocol uses; null when empty.
 * Mirrors the hub, which drops fields the protocol doesn't use.
 */
export function checkPayloadFromForm(
	protocol: MonitorProtocol,
	form: CheckFormState
): { check: MonitorCheckOptions | null; secrets: MonitorHTTPSecrets | null } {
	const check: MonitorCheckOptions = {}
	if (protocol === "dns") {
		if (form.recordType) check.recordType = form.recordType
		if (form.expected.trim()) {
			check.expected = form.expected.trim()
			if (form.matchMode === "equals") check.matchMode = "equals"
		}
	}
	if ((protocol === "tcp" || protocol === "ssh") && form.banner) {
		check.banner = form.banner
	}
	if (supportsTls(protocol)) {
		if (form.tlsMode === "tls") check.tls = true
		else if (form.tlsMode === "starttls" && supportsStartTls(protocol)) check.startTLS = true
		if ((check.tls || check.startTLS) && form.ignoreTLS) check.ignoreTLS = true
	}
	if (protocol === "grpc" && form.service.trim()) {
		check.service = form.service.trim()
	}

	const secrets: MonitorHTTPSecrets = {}
	if (usesCheckCredentials(protocol)) {
		if (form.username.trim()) secrets.username = form.username.trim()
		if (form.password) secrets.password = form.password
	}
	return {
		check: Object.keys(check).length ? check : null,
		secrets: Object.keys(secrets).length ? secrets : null,
	}
}

/** Whether the form sets any check option the protocol uses. */
export function hasCustomCheckOptions(protocol: MonitorProtocol, form: CheckFormState) {
	const { check, secrets } = checkPayloadFromForm(protocol, form)
	return !!check || !!secrets
}

/** Whether a monitor with these options reports a TLS certificate. */
export function monitorReportsCert(protocol: MonitorProtocol, target: string, form: CheckFormState) {
	if (protocol === "http") return isHttpsTarget(target)
	return checkUsesTls(protocol, checkPayloadFromForm(protocol, form).check)
}
