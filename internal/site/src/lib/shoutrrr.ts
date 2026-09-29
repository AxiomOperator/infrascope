/**
 * Form-based builder for Shoutrrr notification URLs.
 *
 * Every service is described declaratively (fields + how they map onto the URL) and exposes
 * `build` / `parse`, which mirror how github.com/nicholas-fedor/shoutrrr v0.21.0 reads its
 * config URLs (userinfo / host / path / query). Query parameters the form does not know about
 * are kept in `extra` and appended again on build, so editing a URL never drops user settings.
 *
 * Labels and messages are lazy (`() => t\`...\``) so they are translated at render time.
 */
import { t } from "@lingui/core/macro"

// ---------------------------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------------------------

export type FieldType = "text" | "password" | "number" | "list" | "pairs" | "select" | "boolean"
export type FieldValue = string | boolean | string[]
export type FieldValues = Record<string, FieldValue>
export type QueryPair = [key: string, value: string]

export interface ServiceValues {
	fields: FieldValues
	/** query parameters the form doesn't model; re-appended verbatim (decoded) on build */
	extra: QueryPair[]
}

export interface FieldOption {
	value: string
	label: () => string
}

export interface Field {
	key: string
	label: () => string
	type: FieldType
	required?: boolean
	placeholder?: string
	help?: () => string
	options?: FieldOption[]
	/** canonical query parameter this field is stored in */
	query?: string
	/** other query keys Shoutrrr accepts for the same setting */
	aliases?: string[]
	/** initial value; for query fields the value is omitted from the URL when equal to it */
	defaultValue?: FieldValue
	/** shown under "More options" */
	advanced?: boolean
	/** hide the field (and skip validation) unless this returns true */
	visible?: (fields: FieldValues) => boolean
	/** extra validation; returns an error message */
	validate?: (value: FieldValue, fields: FieldValues) => string | undefined
	/** item separator for list fields in the URL (default ",") */
	separator?: string
	/** map a raw query value to a field value; return null if it can't be represented */
	fromQuery?: (raw: string) => string | null
}

export interface PasteHelper {
	label: () => string
	placeholder: string
	extract: (input: string) => FieldValues | null
}

export interface ShoutrrrService {
	id: string
	name: string
	docsUrl: string
	schemes: string[]
	fields: Field[]
	paste?: PasteHelper
	defaults(): ServiceValues
	build(values: ServiceValues): string
	parse(url: string): ServiceValues | null
	/** short non-secret description, e.g. "ntfy.sh/alerts" */
	summary(values: ServiceValues): string
	/** strings that must be masked when displaying the URL */
	secrets(values: ServiceValues): string[]
	/** field key -> error message ("" key for errors not tied to a field) */
	validate(values: ServiceValues): Record<string, string>
}

// ---------------------------------------------------------------------------------------------
// Go-compatible URL parsing (mirrors net/url.Parse closely enough for Shoutrrr URLs)
// ---------------------------------------------------------------------------------------------

export interface GoURL {
	scheme: string
	user: { username: string; password: string | null } | null
	/** raw authority (userinfo@host:port) */
	authority: string
	/** host including port, as Go's URL.Host */
	host: string
	hostname: string
	port: string
	/** decoded path */
	path: string
	rawPath: string
	/** decoded query pairs, in order */
	query: QueryPair[]
	rawQuery: string
	/** decoded fragment, null when absent */
	fragment: string | null
}

function decode(s: string): string {
	return decodeURIComponent(s)
}

function decodeQueryComponent(s: string): string {
	return decodeURIComponent(s.replace(/\+/g, " "))
}

/** Parses the query like Go's url.ParseQuery. Throws on malformed escapes. */
export function parseQueryString(raw: string): QueryPair[] {
	const pairs: QueryPair[] = []
	for (const part of raw.split("&")) {
		if (!part) continue
		const eq = part.indexOf("=")
		const rawKey = eq === -1 ? part : part.slice(0, eq)
		// Go rejects (skips) keys containing a semicolon
		if (rawKey.includes(";")) continue
		const rawValue = eq === -1 ? "" : part.slice(eq + 1)
		pairs.push([decodeQueryComponent(rawKey), decodeQueryComponent(rawValue)])
	}
	return pairs
}

export function parseGoURL(input: string): GoURL | null {
	const raw = input.trim()
	const m = /^([a-zA-Z][a-zA-Z0-9+.-]*):\/\/(.*)$/s.exec(raw)
	if (!m || [...raw].some((c) => c.charCodeAt(0) < 0x20 || c.charCodeAt(0) === 0x7f)) return null
	const scheme = m[1].toLowerCase()
	let rest = m[2]
	let fragment: string | null = null
	let rawQuery = ""
	try {
		const hashIdx = rest.indexOf("#")
		if (hashIdx !== -1) {
			fragment = decode(rest.slice(hashIdx + 1))
			rest = rest.slice(0, hashIdx)
		}
		const qIdx = rest.indexOf("?")
		if (qIdx !== -1) {
			rawQuery = rest.slice(qIdx + 1)
			rest = rest.slice(0, qIdx)
		}
		const slashIdx = rest.indexOf("/")
		const authority = slashIdx === -1 ? rest : rest.slice(0, slashIdx)
		const rawPath = slashIdx === -1 ? "" : rest.slice(slashIdx)
		const atIdx = authority.lastIndexOf("@")
		let user: GoURL["user"] = null
		let host = authority
		if (atIdx !== -1) {
			const userinfo = authority.slice(0, atIdx)
			host = authority.slice(atIdx + 1)
			const colon = userinfo.indexOf(":")
			user =
				colon === -1
					? { username: decode(userinfo), password: null }
					: { username: decode(userinfo.slice(0, colon)), password: decode(userinfo.slice(colon + 1)) }
		}
		if (/[@/?#\\\s]/.test(host)) return null
		host = decode(host)
		let hostname = host
		let port = ""
		const portMatch = /^(.*?)(?::(\d*))?$/.exec(host)
		if (host.startsWith("[")) {
			const end = host.indexOf("]")
			hostname = host.slice(0, end + 1)
			port = host.slice(end + 1).replace(/^:/, "")
		} else if (portMatch && host.split(":").length === 2) {
			hostname = portMatch[1]
			port = portMatch[2] ?? ""
		} else if (host.includes(":")) {
			return null
		}
		return {
			scheme,
			user,
			authority,
			host,
			hostname,
			port,
			path: decode(rawPath),
			rawPath,
			query: parseQueryString(rawQuery),
			rawQuery,
			fragment,
		}
	} catch {
		return null
	}
}

// ---------------------------------------------------------------------------------------------
// Encoding helpers
// ---------------------------------------------------------------------------------------------

/** userinfo component: everything but unreserved characters is escaped */
export const encUser = (s: string) => encodeURIComponent(s)

/** a single path segment ("/" is escaped; Go decodes %2F back into the path) */
export function encSegment(s: string): string {
	return encodeURIComponent(s).replace(/%(40|2B|3A|2C|3D|24)/gi, (_, h: string) => decode(`%${h}`))
}

/** a path that may contain "/" separators */
export function encPath(s: string): string {
	return s.split("/").map(encSegment).join("/")
}

/** query key or value */
export function encQuery(s: string): string {
	return encodeURIComponent(s).replace(/%(40|2C|3A|2F|24)/gi, (_, h: string) => decode(`%${h}`))
}

export function buildQuery(pairs: QueryPair[]): string {
	return pairs.map(([k, v]) => `${encQuery(k)}=${encQuery(v)}`).join("&")
}

/** userinfo@host; a null password omits the ":password" part, an empty user with no password omits userinfo */
function authority(user: string, password: string | null, host: string): string {
	if (password !== null) return `${encUser(user)}:${encUser(password)}@${host}`
	if (user) return `${encUser(user)}@${host}`
	return host
}

const optional = (s: string) => (s === "" ? null : s)

// ---------------------------------------------------------------------------------------------
// Value helpers
// ---------------------------------------------------------------------------------------------

export function str(v: FieldValue | undefined): string {
	if (Array.isArray(v)) return v.join(",")
	if (typeof v === "boolean") return v ? "yes" : "no"
	return (v ?? "").trim()
}

export function list(v: FieldValue | undefined): string[] {
	if (Array.isArray(v)) return v.map((s) => s.trim()).filter(Boolean)
	if (typeof v === "string")
		return v
			.split(/[\n,]/)
			.map((s) => s.trim())
			.filter(Boolean)
	return []
}

/** one item per line (for key=value pairs, whose values may contain commas) */
export function lines(v: FieldValue | undefined): string[] {
	const items = Array.isArray(v) ? v : typeof v === "string" ? v.split("\n") : []
	return items.map((s) => s.trim()).filter(Boolean)
}

function bool(v: FieldValue | undefined): boolean {
	return v === true
}

/** Go's format.ParseBool */
export function parseGoBool(v: string): boolean | null {
	switch (v.toLowerCase()) {
		case "true":
		case "1":
		case "yes":
		case "y":
			return true
		case "false":
		case "0":
		case "no":
		case "n":
			return false
		default:
			return null
	}
}

/** Shortens a secret for display: "abcd…" */
export function maskSecret(s: string): string {
	if (!s) return ""
	if (s.length <= 6) return "••••"
	return `${s.slice(0, 4)}…`
}

function emptyValue(field: Field): FieldValue {
	if (field.defaultValue !== undefined)
		return Array.isArray(field.defaultValue) ? [...field.defaultValue] : field.defaultValue
	switch (field.type) {
		case "boolean":
			return false
		case "list":
		case "pairs":
			return []
		default:
			return ""
	}
}

function isEmpty(v: FieldValue | undefined): boolean {
	if (v === undefined || v === "") return true
	if (Array.isArray(v)) return v.every((s) => s.trim() === "")
	if (typeof v === "string") return v.trim() === ""
	return false
}

function sameValue(a: FieldValue | undefined, b: FieldValue | undefined): boolean {
	if (Array.isArray(a) || Array.isArray(b)) return list(a).join("\n") === list(b).join("\n")
	if (typeof a === "boolean" || typeof b === "boolean") return bool(a) === bool(b)
	return str(a) === str(b)
}

// ---------------------------------------------------------------------------------------------
// Validation helpers
// ---------------------------------------------------------------------------------------------

const HOST_RE = /^(\[[0-9a-fA-F:.]+\]|[^\s/?#@\\:[\]]+)(:\d{1,5})?$/
const TOKEN_RE = /^[^\s/?#@\\:]+$/

const vHost = (v: FieldValue) => (HOST_RE.test(str(v)) ? undefined : t`Enter a host name, optionally with :port`)
const vNoSlash = (v: FieldValue) => (/[/\s]/.test(str(v)) ? t`Must not contain "/" or spaces` : undefined)
const vHostToken = (v: FieldValue) =>
	TOKEN_RE.test(str(v)) ? undefined : t`Contains characters that are not allowed here (/ ? # @ : or spaces)`
const vNumber = (min: number, max: number) => (v: FieldValue) => {
	const s = str(v)
	if (!s) return undefined
	const n = Number(s)
	return /^-?\d+$/.test(s) && n >= min && n <= max ? undefined : t`Enter a whole number between ${min} and ${max}`
}
const vPort = vNumber(1, 65535)
const PHONE_RE = /^\+?[0-9\s)(+-]+$/
const vPhone = (v: FieldValue) =>
	PHONE_RE.test(str(v)) ? undefined : t`Enter a phone number with country code, e.g. +15551234567`
const vHttpUrl = (v: FieldValue) => {
	const u = parseGoURL(str(v))
	return u && (u.scheme === "https" || u.scheme === "http") && u.hostname ? undefined : t`Enter a full http(s):// URL`
}

// ---------------------------------------------------------------------------------------------
// Service factory
// ---------------------------------------------------------------------------------------------

interface BaseResult {
	/** URL up to (not including) "?" */
	url: string
	/** query pairs placed before the field/extra pairs */
	query?: QueryPair[]
	fragment?: string
}

interface ParseBaseResult {
	fields: FieldValues
	/** query pairs left for the standard field/extra processing (defaults to all) */
	rest?: QueryPair[]
}

interface ServiceSpec {
	id: string
	name: string
	schemes: string[]
	docs?: string
	fields: Field[]
	paste?: PasteHelper
	/** match query keys exactly instead of case-insensitively (services that don't use the prop resolver) */
	caseSensitive?: boolean
	base(fields: FieldValues): BaseResult
	parseBase(u: GoURL): ParseBaseResult | null
	summary(fields: FieldValues): string
	secretParts?(fields: FieldValues): string[]
	check?(fields: FieldValues): Record<string, string | undefined>
}

function defineService(spec: ServiceSpec): ShoutrrrService {
	const queryFields = spec.fields.filter((f) => f.query)

	const defaults = (): ServiceValues => {
		const fields: FieldValues = {}
		for (const f of spec.fields) fields[f.key] = emptyValue(f)
		return { fields, extra: [] }
	}

	const build = (values: ServiceValues): string => {
		const fields = { ...defaults().fields, ...values.fields }
		const base = spec.base(fields)
		const pairs: QueryPair[] = [...(base.query ?? [])]
		for (const f of queryFields) {
			if (f.visible && !f.visible(fields)) continue
			const value = fields[f.key]
			if (f.type === "boolean") {
				if (!sameValue(value, f.defaultValue ?? false)) pairs.push([f.query as string, bool(value) ? "yes" : "no"])
				continue
			}
			if (isEmpty(value) || (f.defaultValue !== undefined && sameValue(value, f.defaultValue))) continue
			const s = f.type === "list" ? list(value).join(f.separator ?? ",") : str(value)
			pairs.push([f.query as string, s])
		}
		pairs.push(...values.extra)
		const q = buildQuery(pairs)
		return `${base.url}${q ? `?${q}` : ""}${base.fragment ? `#${base.fragment}` : ""}`
	}

	const parse = (url: string): ServiceValues | null => {
		const u = parseGoURL(url)
		if (!u || !spec.schemes.includes(u.scheme)) return null
		const parsed = spec.parseBase(u)
		if (!parsed) return null
		const values = defaults()
		Object.assign(values.fields, parsed.fields)
		const seen = new Set<string>()
		for (const [key, raw] of parsed.rest ?? u.query) {
			const k = spec.caseSensitive ? key : key.toLowerCase()
			const field = queryFields.find((f) => {
				const keys = [f.query as string, ...(f.aliases ?? [])]
				return keys.some((q) => (spec.caseSensitive ? q === k : q.toLowerCase() === k))
			})
			// duplicates: Shoutrrr only reads the first value, keep the others verbatim
			if (!field || seen.has(field.key)) {
				values.extra.push([key, raw])
				continue
			}
			seen.add(field.key)
			let value: FieldValue
			switch (field.type) {
				case "boolean": {
					const b = parseGoBool(raw)
					if (b === null) return null
					value = b
					break
				}
				case "list":
					value = raw
						.split(field.separator ?? ",")
						.map((s) => s.trim())
						.filter(Boolean)
					break
				case "select": {
					const v = field.fromQuery ? field.fromQuery(raw) : raw
					if (v === null || !field.options?.some((o) => o.value === v)) return null
					value = v
					break
				}
				default: {
					const v = field.fromQuery ? field.fromQuery(raw) : raw
					if (v === null) return null
					value = v
				}
			}
			values.fields[field.key] = value
		}
		return values
	}

	const validate = (values: ServiceValues): Record<string, string> => {
		const fields = { ...defaults().fields, ...values.fields }
		const errors: Record<string, string> = {}
		for (const f of spec.fields) {
			if (f.visible && !f.visible(fields)) continue
			const value = fields[f.key]
			if (isEmpty(value)) {
				if (f.required) errors[f.key] = t`Required`
				continue
			}
			if (f.type === "number") {
				const err = vNumber(-2147483648, 2147483647)(value)
				if (err) {
					errors[f.key] = err
					continue
				}
			}
			const err = f.validate?.(value, fields)
			if (err) errors[f.key] = err
		}
		for (const [key, msg] of Object.entries(spec.check?.(fields) ?? {})) if (msg) errors[key] ??= msg
		return errors
	}

	const secrets = (values: ServiceValues): string[] => {
		const fields = { ...defaults().fields, ...values.fields }
		const out: string[] = []
		for (const f of spec.fields) {
			if (f.type !== "password") continue
			const v = fields[f.key]
			out.push(...(Array.isArray(v) ? v : [str(v)]))
		}
		out.push(...(spec.secretParts?.(fields) ?? []))
		return out.filter((s) => s.length > 0)
	}

	return {
		id: spec.id,
		name: spec.name,
		docsUrl: spec.docs ?? `https://beszel.dev/guide/notifications/${spec.id}`,
		schemes: spec.schemes,
		fields: spec.fields,
		paste: spec.paste,
		defaults,
		build,
		parse,
		summary: (values) => spec.summary({ ...defaults().fields, ...values.fields }),
		secrets,
		validate,
	}
}

// ---------------------------------------------------------------------------------------------
// Shared field builders
// ---------------------------------------------------------------------------------------------

const hostField = (overrides: Partial<Field> = {}): Field => ({
	key: "host",
	label: () => t`Host`,
	type: "text",
	required: true,
	placeholder: "example.com",
	validate: vHost,
	...overrides,
})

const disableTlsField = (query = "disabletls"): Field => ({
	key: "disableTls",
	label: () => t`Use plain HTTP (disable TLS)`,
	type: "boolean",
	query,
	defaultValue: false,
	advanced: true,
})

/** strips a known URL prefix and returns the remaining path segments */
function webhookSegments(input: string, hostRe: RegExp, prefix: string): { u: GoURL; segs: string[] } | null {
	const u = parseGoURL(input)
	if (!u || (u.scheme !== "https" && u.scheme !== "http") || !hostRe.test(u.hostname)) return null
	const path = u.path.replace(/\/+$/, "")
	if (!path.startsWith(prefix)) return null
	return { u, segs: path.slice(prefix.length).split("/").filter(Boolean) }
}

// ---------------------------------------------------------------------------------------------
// Paste helpers (exported for tests)
// ---------------------------------------------------------------------------------------------

/** https://discord.com/api/webhooks/<id>/<token> */
export function parseDiscordWebhookUrl(input: string): { webhookId: string; token: string } | null {
	const r = webhookSegments(input.trim(), /^((canary|ptb)\.)?discord(app)?\.com$/i, "/api/webhooks/")
	if (!r || r.segs.length < 2) return null
	return { webhookId: r.segs[0], token: r.segs[1] }
}

/** https://hooks.slack.com/services/T000/B000/XXXX -> "T000/B000/XXXX" */
export function parseSlackWebhookUrl(input: string): string | null {
	const r = webhookSegments(input.trim(), /^hooks\.slack\.com$/i, "/services/")
	if (!r || r.segs.length !== 3) return null
	return r.segs.join("/")
}

const SLACK_TOKEN_RE = /(?:(xox.|hook)[-:]|:?)([A-Z0-9]{9,})([-/,])([A-Z0-9]{9,})([-/,])([A-Za-z0-9]{24,})/

/** Normalizes any accepted Slack token / webhook URL form to Shoutrrr's "type:p1-p2-p3" */
export function parseSlackToken(input: string): { type: string; parts: [string, string, string] } | null {
	const s = input.trim()
	const fromHook = parseSlackWebhookUrl(s)
	const m = SLACK_TOKEN_RE.exec(fromHook ?? s)
	if (!m || m[3] !== m[5]) return null
	return { type: m[1] || "hook", parts: [m[2], m[4], m[6]] }
}

// ---------------------------------------------------------------------------------------------
// Services
// ---------------------------------------------------------------------------------------------

const GENERIC_CONFIG_KEYS = ["contenttype", "disabletls", "template", "title", "titlekey", "messagekey", "method"]
const GO_KEY_PREFIX = "__"

const generic = defineService({
	id: "generic",
	name: "Generic Webhook",
	schemes: ["generic", "generic+https", "generic+http"],
	caseSensitive: true,
	fields: [
		{
			key: "url",
			label: () => t`Webhook URL`,
			type: "text",
			required: true,
			placeholder: "https://example.com/webhook",
			help: () => t`The full URL requests are sent to. Use http:// to disable TLS.`,
			validate: (v) => {
				const err = vHttpUrl(v)
				if (err) return err
				const u = parseGoURL(str(v)) as GoURL
				if (u.fragment !== null) return t`URL fragments (#…) are not supported`
				if (u.query.some(([k]) => k.length > 1 && (k[0] === "@" || k[0] === "$")))
					return t`Query parameters starting with @ or $ are reserved for headers and extra data`
				return undefined
			},
		},
		{
			key: "template",
			label: () => t`Payload`,
			type: "select",
			query: "template",
			defaultValue: "",
			options: [
				{ value: "", label: () => t`Plain text` },
				{ value: "json", label: () => t`JSON` },
			],
		},
		{
			key: "titleKey",
			label: () => t`JSON title key`,
			type: "text",
			query: "titlekey",
			defaultValue: "title",
			advanced: true,
			visible: (f) => f.template === "json",
		},
		{
			key: "messageKey",
			label: () => t`JSON message key`,
			type: "text",
			query: "messagekey",
			defaultValue: "message",
			advanced: true,
			visible: (f) => f.template === "json",
		},
		{
			key: "method",
			label: () => t`HTTP method`,
			type: "select",
			query: "method",
			defaultValue: "POST",
			advanced: true,
			fromQuery: (v) => v.toUpperCase(),
			options: ["POST", "PUT", "PATCH", "GET"].map((m) => ({ value: m, label: () => m })),
		},
		{
			key: "contentType",
			label: () => t`Content-Type`,
			type: "text",
			query: "contenttype",
			defaultValue: "application/json",
			advanced: true,
		},
		{
			key: "headers",
			label: () => t`Custom headers`,
			type: "pairs",
			placeholder: "Authorization=Bearer abc123",
			help: () => t`One per line as Name=Value.`,
			advanced: true,
			validate: (v) => validatePairs(v),
		},
		{
			key: "data",
			label: () => t`Extra JSON data`,
			type: "pairs",
			placeholder: "source=infrascope",
			help: () => t`One per line as key=value. Added to JSON payloads.`,
			advanced: true,
			validate: (v) => validatePairs(v),
		},
	],
	base(f) {
		const u = parseGoURL(str(f.url))
		if (!u) return { url: "generic://" }
		const query: QueryPair[] = u.query.map(([k, v]) => [GENERIC_CONFIG_KEYS.includes(k) ? GO_KEY_PREFIX + k : k, v])
		if (u.scheme === "http") query.push(["disabletls", "yes"])
		for (const [prefix, key] of [
			["@", "headers"],
			["$", "data"],
		]) {
			for (const pair of lines(f[key])) {
				const eq = pair.indexOf("=")
				if (eq > 0) query.push([prefix + pair.slice(0, eq).trim(), pair.slice(eq + 1).trim()])
			}
		}
		// keep the webhook's own authority/path exactly as entered
		return { url: `generic://${u.authority}${u.rawPath}`, query }
	},
	parseBase(u) {
		let disableTls = u.scheme === "generic+http"
		const webhookQuery: QueryPair[] = []
		const rest: QueryPair[] = []
		const headers: string[] = []
		const data: string[] = []
		for (const [k, v] of u.query) {
			if (k.length > 1 && k[0] === "@") headers.push(`${k.slice(1)}=${v}`)
			else if (k.length > 1 && k[0] === "$") data.push(`${k.slice(1)}=${v}`)
			else if (k === "disabletls") {
				const b = parseGoBool(v)
				if (b === null) return null
				disableTls = b
			} else if (GENERIC_CONFIG_KEYS.includes(k)) rest.push([k, v])
			else {
				const unescaped = k.slice(GO_KEY_PREFIX.length)
				const isEscaped = k.startsWith(GO_KEY_PREFIX) && GENERIC_CONFIG_KEYS.includes(unescaped)
				webhookQuery.push([isEscaped ? unescaped : k, v])
			}
		}
		if (u.fragment !== null) return null
		const q = buildQuery(webhookQuery)
		const url = `${disableTls ? "http" : "https"}://${u.authority}${u.rawPath}${q ? `?${q}` : ""}`
		return { fields: { url, headers, data }, rest }
	},
	summary: (f) => {
		const u = parseGoURL(str(f.url))
		return u ? `${u.hostname}${u.path.length > 1 ? u.path : ""}` : ""
	},
})

function validatePairs(v: FieldValue): string | undefined {
	return lines(v).some((p) => !/^[^=\s]+=/.test(p)) ? t`Each line must look like key=value` : undefined
}

const bark = defineService({
	id: "bark",
	name: "Bark",
	schemes: ["bark"],
	fields: [
		{ key: "deviceKey", label: () => t`Device key`, type: "password", required: true },
		hostField({ defaultValue: "api.day.app", placeholder: "api.day.app" }),
		{ key: "path", label: () => t`Server path`, type: "text", placeholder: "/", advanced: true },
		{
			key: "scheme",
			label: () => t`Protocol`,
			type: "select",
			query: "scheme",
			defaultValue: "https",
			advanced: true,
			options: [
				{ value: "https", label: () => "HTTPS" },
				{ value: "http", label: () => "HTTP" },
			],
		},
		{ key: "sound", label: () => t`Sound`, type: "text", query: "sound", placeholder: "alarm", advanced: true },
		{ key: "group", label: () => t`Group`, type: "text", query: "group", advanced: true },
		{ key: "icon", label: () => t`Icon URL`, type: "text", query: "icon", advanced: true },
	],
	base: (f) => {
		const path = str(f.path)
		return {
			url: `bark://${authority("", str(f.deviceKey), str(f.host))}${encPath(path ? (path.startsWith("/") ? path : `/${path}`) : "/")}`,
		}
	},
	parseBase(u) {
		// Shoutrrr reads the device key from the password part only
		if (u.user?.password === null || (u.user && u.user.username !== "")) return null
		return { fields: { deviceKey: u.user?.password ?? "", host: u.host, path: u.path === "/" ? "" : u.path } }
	},
	summary: (f) => str(f.host),
})

const discord = defineService({
	id: "discord",
	name: "Discord",
	schemes: ["discord"],
	fields: [
		{
			key: "webhookId",
			label: () => t`Webhook ID`,
			type: "text",
			required: true,
			placeholder: "693853386302554172",
			validate: (v) => (/^\d+$/.test(str(v)) ? undefined : t`The webhook ID is a number`),
		},
		{ key: "token", label: () => t`Webhook token`, type: "password", required: true, validate: vNoSlash },
		{ key: "username", label: () => t`Username override`, type: "text", query: "username", advanced: true },
		{
			key: "avatar",
			label: () => t`Avatar URL`,
			type: "text",
			query: "avatar",
			aliases: ["avatarurl"],
			advanced: true,
		},
		{ key: "threadId", label: () => t`Thread ID`, type: "text", query: "thread_id", advanced: true },
		{
			key: "splitLines",
			label: () => t`Send each line as a separate embed`,
			type: "boolean",
			query: "splitlines",
			defaultValue: true,
			advanced: true,
		},
		{ key: "raw", label: () => t`Send message as raw JSON payload`, type: "boolean", advanced: true },
	],
	paste: {
		label: () => t`Paste the Discord webhook URL`,
		placeholder: "https://discord.com/api/webhooks/…",
		extract: (input) => parseDiscordWebhookUrl(input),
	},
	base: (f) => ({ url: `discord://${authority(str(f.token), null, str(f.webhookId))}${bool(f.raw) ? "/raw" : ""}` }),
	parseBase(u) {
		if (u.path !== "" && u.path !== "/raw") return null
		if (u.user?.password != null) return null
		return { fields: { webhookId: u.host, token: u.user?.username ?? "", raw: u.path === "/raw" } }
	},
	summary: (f) => t`webhook ${maskSecret(str(f.webhookId))}`,
})

const gotify = defineService({
	id: "gotify",
	name: "Gotify",
	schemes: ["gotify"],
	fields: [
		hostField({ placeholder: "gotify.example.com" }),
		{ key: "token", label: () => t`Application token`, type: "password", required: true, validate: vNoSlash },
		{ key: "path", label: () => t`Server subpath`, type: "text", placeholder: "/gotify", advanced: true },
		{
			key: "priority",
			label: () => t`Priority`,
			type: "number",
			query: "priority",
			defaultValue: "0",
			placeholder: "0",
			validate: vNumber(-2, 10),
		},
		disableTlsField(),
		{
			key: "insecureSkipVerify",
			label: () => t`Skip TLS certificate verification`,
			type: "boolean",
			query: "insecureskipverify",
			defaultValue: false,
			advanced: true,
		},
		{
			key: "useHeader",
			label: () => t`Send token in a header instead of the URL`,
			type: "boolean",
			query: "useheader",
			defaultValue: false,
			advanced: true,
		},
	],
	base: (f) => {
		const sub = str(f.path).replace(/^\/+|\/+$/g, "")
		return { url: `gotify://${str(f.host)}/${sub ? `${encPath(sub)}/` : ""}${encSegment(str(f.token))}` }
	},
	parseBase(u) {
		if (u.user) return null
		const path = u.path.replace(/\/$/, "")
		const idx = path.lastIndexOf("/") + 1
		return { fields: { host: u.host, token: path.slice(idx), path: path.slice(0, idx).replace(/^\/+|\/+$/g, "") } }
	},
	summary: (f) => str(f.host),
})

const googlechat = defineService({
	id: "googlechat",
	name: "Google Chat",
	schemes: ["googlechat", "hangouts"],
	caseSensitive: true,
	fields: [
		{
			key: "path",
			label: () => t`Space path`,
			type: "text",
			required: true,
			placeholder: "/v1/spaces/AAAA…/messages",
		},
		{ key: "key", label: () => t`Key`, type: "password", required: true },
		{ key: "token", label: () => t`Token`, type: "password", required: true },
		hostField({ defaultValue: "chat.googleapis.com", advanced: true }),
	],
	paste: {
		label: () => t`Paste the Google Chat webhook URL`,
		placeholder: "https://chat.googleapis.com/v1/spaces/…/messages?key=…&token=…",
		extract(input) {
			const u = parseGoURL(input.trim())
			if (!u || u.scheme !== "https" || !u.path.startsWith("/v1/spaces/")) return null
			const get = (k: string) => u.query.find(([q]) => q === k)?.[1] ?? ""
			if (!get("key") || !get("token")) return null
			return { host: u.host, path: u.path, key: get("key"), token: get("token") }
		},
	},
	base: (f) => {
		const path = str(f.path)
		return {
			url: `googlechat://${str(f.host)}${encPath(path.startsWith("/") ? path : `/${path}`)}`,
			query: [
				["key", str(f.key)],
				["token", str(f.token)],
			],
		}
	},
	parseBase(u) {
		if (u.user) return null
		const get = (k: string) => u.query.find(([q]) => q === k)?.[1] ?? ""
		return {
			fields: { host: u.host, path: u.path, key: get("key"), token: get("token") },
			rest: u.query.filter(([k]) => k !== "key" && k !== "token"),
		}
	},
	summary: (f) => /\/spaces\/([^/]+)/.exec(str(f.path))?.[1] ?? str(f.host),
})

const ifttt = defineService({
	id: "ifttt",
	name: "IFTTT",
	schemes: ["ifttt"],
	fields: [
		{ key: "webhookKey", label: () => t`Webhooks key`, type: "password", required: true, validate: vHostToken },
		{ key: "events", label: () => t`Event names`, type: "list", query: "events", required: true },
		{ key: "value1", label: () => t`Value 1`, type: "text", query: "value1", advanced: true },
		{ key: "value2", label: () => t`Value 2`, type: "text", query: "value2", advanced: true },
		{ key: "value3", label: () => t`Value 3`, type: "text", query: "value3", advanced: true },
		{
			key: "messageValue",
			label: () => t`Send message as`,
			type: "select",
			query: "messagevalue",
			defaultValue: "2",
			advanced: true,
			options: ["1", "2", "3"].map((n) => ({ value: n, label: () => t`Value ${n}` })),
		},
		{
			key: "titleValue",
			label: () => t`Send title as`,
			type: "select",
			query: "titlevalue",
			defaultValue: "0",
			advanced: true,
			options: [
				{ value: "0", label: () => t`Don't send` },
				...["1", "2", "3"].map((n) => ({ value: n, label: () => t`Value ${n}` })),
			],
		},
	],
	base: (f) => ({ url: `ifttt://${str(f.webhookKey)}/` }),
	parseBase(u) {
		if (u.user || u.port) return null
		return { fields: { webhookKey: u.hostname } }
	},
	check: (f) =>
		str(f.titleValue) !== "0" && str(f.titleValue) === str(f.messageValue)
			? { titleValue: t`Title and message can't use the same value` }
			: {},
	summary: (f) => list(f.events).join(", "),
})

const join = defineService({
	id: "join",
	name: "Join",
	schemes: ["join"],
	fields: [
		{ key: "apiKey", label: () => t`API key`, type: "password", required: true },
		{ key: "devices", label: () => t`Device IDs`, type: "list", query: "devices", required: true },
		{ key: "icon", label: () => t`Icon URL`, type: "text", query: "icon", advanced: true },
	],
	base: (f) => ({ url: `join://${authority("shoutrrr", str(f.apiKey), "join")}/` }),
	parseBase(u) {
		if (u.user?.password == null) return null
		return { fields: { apiKey: u.user.password } }
	},
	summary: (f) => list(f.devices).join(", "),
})

const lark = defineService({
	id: "lark",
	name: "Lark / Feishu",
	schemes: ["lark"],
	fields: [
		{
			key: "host",
			label: () => t`Platform`,
			type: "select",
			defaultValue: "open.larksuite.com",
			options: [
				{ value: "open.larksuite.com", label: () => "Lark (open.larksuite.com)" },
				{ value: "open.feishu.cn", label: () => "Feishu (open.feishu.cn)" },
			],
		},
		{ key: "token", label: () => t`Bot token`, type: "password", required: true, validate: vNoSlash },
		{ key: "secret", label: () => t`Signing secret`, type: "password", query: "secret" },
	],
	paste: {
		label: () => t`Paste the bot webhook URL`,
		placeholder: "https://open.larksuite.com/open-apis/bot/v2/hook/…",
		extract(input) {
			const r = webhookSegments(input.trim(), /^open\.(larksuite\.com|feishu\.cn)$/i, "/open-apis/bot/v2/hook/")
			return r && r.segs.length === 1 ? { host: r.u.hostname.toLowerCase(), token: r.segs[0] } : null
		},
	},
	base: (f) => ({ url: `lark://${str(f.host)}/${encSegment(str(f.token))}` }),
	parseBase(u) {
		if (u.user || !["open.larksuite.com", "open.feishu.cn"].includes(u.host)) return null
		return { fields: { host: u.host, token: u.path.replace(/^\/+|\/+$/g, "") } }
	},
	summary: (f) => str(f.host),
})

const mattermost = defineService({
	id: "mattermost",
	name: "Mattermost",
	schemes: ["mattermost"],
	fields: [
		hostField({ placeholder: "mattermost.example.com" }),
		{ key: "token", label: () => t`Webhook token`, type: "password", required: true, validate: vNoSlash },
		{ key: "channel", label: () => t`Channel override`, type: "text", placeholder: "town-square", validate: vNoSlash },
		{ key: "username", label: () => t`Username override`, type: "text" },
		{
			key: "icon",
			label: () => t`Icon`,
			type: "text",
			query: "icon",
			aliases: ["icon_emoji", "icon_url"],
			help: () => t`Emoji name or image URL`,
			advanced: true,
		},
		disableTlsField(),
	],
	paste: {
		label: () => t`Paste the incoming webhook URL`,
		placeholder: "https://mattermost.example.com/hooks/…",
		extract(input) {
			const u = parseGoURL(input.trim())
			if (!u || (u.scheme !== "https" && u.scheme !== "http")) return null
			const m = /^(.*)\/hooks\/([^/]+)\/?$/.exec(u.path)
			if (!m || m[1]) return null
			return { host: u.host, token: m[2], disableTls: u.scheme === "http" }
		},
	},
	base: (f) => {
		const channel = str(f.channel)
		return {
			url: `mattermost://${authority(str(f.username), null, str(f.host))}/${encSegment(str(f.token))}${channel ? `/${encSegment(channel)}` : ""}`,
		}
	},
	parseBase(u) {
		if (u.user?.password != null) return null
		const segs = u.path.replace(/^\/+|\/+$/g, "").split("/")
		if (segs.length > 2) return null
		return { fields: { host: u.host, username: u.user?.username ?? "", token: segs[0] ?? "", channel: segs[1] ?? "" } }
	},
	summary: (f) => (str(f.channel) ? `${str(f.host)} · ${str(f.channel)}` : str(f.host)),
})

const matrix = defineService({
	id: "matrix",
	name: "Matrix",
	schemes: ["matrix"],
	fields: [
		hostField({ placeholder: "matrix.org" }),
		{
			key: "user",
			label: () => t`Username`,
			type: "text",
			help: () => t`Leave empty when using an access token.`,
		},
		{ key: "password", label: () => t`Password or access token`, type: "password", required: true },
		{
			key: "rooms",
			label: () => t`Rooms`,
			type: "list",
			query: "rooms",
			aliases: ["room"],
			placeholder: "!roomID:matrix.org, #alias:matrix.org",
			help: () => t`Room IDs (starting with !) or aliases. Leave empty to post to all joined rooms.`,
		},
		disableTlsField("disableTLS"),
	],
	base: (f) => ({ url: `matrix://${authority(str(f.user), str(f.password), str(f.host))}/` }),
	parseBase(u) {
		if (u.path !== "" && u.path !== "/") return null
		return { fields: { host: u.host, user: u.user?.username ?? "", password: u.user?.password ?? "" } }
	},
	summary: (f) => (list(f.rooms).length ? `${str(f.host)} · ${list(f.rooms).join(", ")}` : str(f.host)),
})

const mqtt = defineService({
	id: "mqtt",
	name: "MQTT",
	schemes: ["mqtt", "mqtts"],
	fields: [
		hostField({ placeholder: "broker.example.com" }),
		{ key: "tls", label: () => t`Use TLS (mqtts)`, type: "boolean" },
		{ key: "port", label: () => t`Port`, type: "number", placeholder: "1883 / 8883", validate: vPort },
		{
			key: "topic",
			label: () => t`Topic`,
			type: "text",
			required: true,
			placeholder: "infrascope/alerts",
			validate: (v) => (/[#+]/.test(str(v)) ? t`Wildcards (+, #) can't be used when publishing` : undefined),
		},
		{ key: "username", label: () => t`Username`, type: "text" },
		{
			key: "password",
			label: () => t`Password`,
			type: "password",
			validate: (_, f) => (str(f.username) ? undefined : t`A password requires a username`),
		},
		{
			key: "qos",
			label: () => t`QoS`,
			type: "select",
			query: "qos",
			defaultValue: "0",
			fromQuery: (v) => ({ at_most_once: "0", at_least_once: "1", exactly_once: "2" })[v.toLowerCase()] ?? v,
			options: [
				{ value: "0", label: () => t`0 – at most once` },
				{ value: "1", label: () => t`1 – at least once` },
				{ value: "2", label: () => t`2 – exactly once` },
			],
		},
		{ key: "retained", label: () => t`Retain message`, type: "boolean", query: "retained", defaultValue: false },
		{
			key: "clientId",
			label: () => t`Client ID`,
			type: "text",
			query: "clientid",
			defaultValue: "shoutrrr",
			advanced: true,
		},
		{
			key: "disableTlsVerification",
			label: () => t`Skip TLS certificate verification`,
			type: "boolean",
			query: "disabletlsverification",
			defaultValue: false,
			advanced: true,
			visible: (f) => bool(f.tls),
		},
	],
	base: (f) => {
		const port = str(f.port)
		const scheme = bool(f.tls) ? "mqtts" : "mqtt"
		return {
			url: `${scheme}://${authority(str(f.username), optional(str(f.password)), str(f.host) + (port ? `:${port}` : ""))}/${encPath(str(f.topic).replace(/^\/+/, ""))}`,
		}
	},
	parseBase(u) {
		return {
			fields: {
				tls: u.scheme === "mqtts",
				host: u.hostname,
				port: u.port,
				topic: u.path.replace(/^\//, ""),
				username: u.user?.username ?? "",
				password: u.user?.password ?? "",
			},
		}
	},
	summary: (f) => `${str(f.host)}/${str(f.topic)}`,
})

const NTFY_PRIORITIES: Record<string, string> = { min: "1", low: "2", default: "3", high: "4", max: "5", urgent: "5" }

const ntfy = defineService({
	id: "ntfy",
	name: "ntfy",
	schemes: ["ntfy"],
	fields: [
		hostField({ defaultValue: "ntfy.sh", placeholder: "ntfy.sh" }),
		{
			key: "topic",
			label: () => t`Topic`,
			type: "text",
			required: true,
			placeholder: "infrascope-alerts",
			validate: vNoSlash,
		},
		{
			key: "username",
			label: () => t`Username`,
			type: "text",
			help: () => t`Leave empty when using an access token.`,
		},
		{ key: "password", label: () => t`Password or access token`, type: "password" },
		{
			key: "priority",
			label: () => t`Priority`,
			type: "select",
			query: "priority",
			defaultValue: "3",
			fromQuery: (v) => NTFY_PRIORITIES[v.toLowerCase()] ?? v,
			options: [
				{ value: "1", label: () => t`1 – min` },
				{ value: "2", label: () => t`2 – low` },
				{ value: "3", label: () => t`3 – default` },
				{ value: "4", label: () => t`4 – high` },
				{ value: "5", label: () => t`5 – max / urgent` },
			],
		},
		{ key: "tags", label: () => t`Tags`, type: "list", query: "tags", placeholder: "warning, computer" },
		{ key: "click", label: () => t`Click URL`, type: "text", query: "click", advanced: true },
		{ key: "icon", label: () => t`Icon URL`, type: "text", query: "icon", advanced: true },
		{
			key: "markdown",
			label: () => t`Markdown formatting`,
			type: "boolean",
			query: "markdown",
			defaultValue: false,
			advanced: true,
		},
		{
			key: "scheme",
			label: () => t`Protocol`,
			type: "select",
			query: "scheme",
			defaultValue: "https",
			advanced: true,
			options: [
				{ value: "https", label: () => "HTTPS" },
				{ value: "http", label: () => "HTTP" },
			],
		},
		{
			key: "disableTlsVerification",
			label: () => t`Skip TLS certificate verification`,
			type: "boolean",
			query: "disabletlsverification",
			defaultValue: false,
			advanced: true,
		},
	],
	base: (f) => ({
		url: `ntfy://${authority(str(f.username), optional(str(f.password)), str(f.host))}/${encSegment(str(f.topic))}`,
	}),
	parseBase(u) {
		return {
			fields: {
				host: u.host,
				topic: u.path.replace(/^\//, ""),
				username: u.user?.username ?? "",
				password: u.user?.password ?? "",
			},
		}
	},
	summary: (f) => `${str(f.host)}/${str(f.topic)}`,
})

const OPSGENIE_ENTITY_RE = /^[^:]+:[^:]+$/
const vEntities = (v: FieldValue) =>
	list(v).every((e) => OPSGENIE_ENTITY_RE.test(e))
		? undefined
		: t`Use type:name, e.g. team:ops or user:jane@example.com`

const opsgenie = defineService({
	id: "opsgenie",
	name: "OpsGenie",
	schemes: ["opsgenie"],
	fields: [
		{
			key: "host",
			label: () => t`Region`,
			type: "select",
			defaultValue: "api.opsgenie.com",
			options: [
				{ value: "api.opsgenie.com", label: () => t`US (api.opsgenie.com)` },
				{ value: "api.eu.opsgenie.com", label: () => t`EU (api.eu.opsgenie.com)` },
			],
		},
		{ key: "apiKey", label: () => t`API key`, type: "password", required: true, validate: vNoSlash },
		{
			key: "responders",
			label: () => t`Responders`,
			type: "list",
			query: "responders",
			placeholder: "team:ops, user:jane@example.com",
			validate: vEntities,
		},
		{
			key: "visibleTo",
			label: () => t`Visible to`,
			type: "list",
			query: "visibleTo",
			placeholder: "team:ops",
			advanced: true,
			validate: vEntities,
		},
		{
			key: "priority",
			label: () => t`Priority`,
			type: "select",
			query: "priority",
			defaultValue: "",
			options: [
				{ value: "", label: () => t`Default` },
				...["P1", "P2", "P3", "P4", "P5"].map((p) => ({ value: p, label: () => p })),
			],
		},
		{ key: "tags", label: () => t`Tags`, type: "list", query: "tags", advanced: true },
		{ key: "source", label: () => t`Source`, type: "text", query: "source", advanced: true },
		{ key: "entity", label: () => t`Entity`, type: "text", query: "entity", advanced: true },
	],
	base: (f) => ({ url: `opsgenie://${str(f.host)}/${encSegment(str(f.apiKey))}` }),
	parseBase(u) {
		if (u.user || !["api.opsgenie.com", "api.eu.opsgenie.com"].includes(u.hostname)) return null
		if (u.port && u.port !== "443") return null
		return { fields: { host: u.hostname, apiKey: u.path.slice(1) } }
	},
	summary: (f) => (list(f.responders).length ? list(f.responders).join(", ") : str(f.host)),
})

const pushbullet = defineService({
	id: "pushbullet",
	name: "Pushbullet",
	schemes: ["pushbullet"],
	fields: [
		{
			key: "token",
			label: () => t`Access token`,
			type: "password",
			required: true,
			validate: (v) => (/^[^\s/?#@:]{34}$/.test(str(v)) ? undefined : t`Pushbullet access tokens are 34 characters`),
		},
		{
			key: "targets",
			label: () => t`Targets`,
			type: "list",
			placeholder: "device, #channel, user@example.com",
			help: () => t`Device names, #channel tags or email addresses. Leave empty to push to all devices.`,
			validate: (v) => (list(v).some((s) => s.includes("/")) ? t`Must not contain "/" or spaces` : undefined),
		},
	],
	base: (f) => {
		const targets = list(f.targets)
		return { url: `pushbullet://${str(f.token)}${targets.length ? `/${targets.map(encSegment).join("/")}` : ""}` }
	},
	parseBase(u) {
		if (u.user || u.port) return null
		let path = u.path.replace(/^\//, "")
		if (u.fragment) path += `/#${u.fragment}`
		return { fields: { token: u.hostname, targets: path.split("/").filter(Boolean) } }
	},
	summary: (f) => (list(f.targets).length ? list(f.targets).join(", ") : t`all devices`),
})

const pushover = defineService({
	id: "pushover",
	name: "Pushover",
	schemes: ["pushover"],
	fields: [
		{ key: "userKey", label: () => t`User key`, type: "text", required: true, validate: vHostToken },
		{ key: "token", label: () => t`API token`, type: "password", required: true },
		{
			key: "devices",
			label: () => t`Devices`,
			type: "list",
			query: "devices",
			help: () => t`Leave empty to send to all devices.`,
		},
		{
			key: "priority",
			label: () => t`Priority`,
			type: "select",
			query: "priority",
			defaultValue: "0",
			options: [
				{ value: "-2", label: () => t`-2 – lowest` },
				{ value: "-1", label: () => t`-1 – low` },
				{ value: "0", label: () => t`0 – normal` },
				{ value: "1", label: () => t`1 – high` },
			],
		},
	],
	base: (f) => ({ url: `pushover://${authority("shoutrrr", str(f.token), str(f.userKey))}/` }),
	parseBase(u) {
		if (u.user?.password == null || u.port) return null
		if (u.path !== "" && u.path !== "/") return null
		return { fields: { userKey: u.host, token: u.user.password } }
	},
	summary: (f) => t`user ${maskSecret(str(f.userKey))}`,
	secretParts: (f) => [str(f.userKey)],
})

const rocketchat = defineService({
	id: "rocketchat",
	name: "Rocket.Chat",
	schemes: ["rocketchat"],
	fields: [
		hostField({ placeholder: "chat.example.com" }),
		{ key: "tokenA", label: () => t`Webhook token (part 1)`, type: "password", required: true, validate: vNoSlash },
		{ key: "tokenB", label: () => t`Webhook token (part 2)`, type: "password", required: true, validate: vNoSlash },
		{
			key: "channel",
			label: () => t`Channel or user`,
			type: "text",
			placeholder: "#general or @user",
			validate: vNoSlash,
		},
		{ key: "username", label: () => t`Username override`, type: "text" },
	],
	paste: {
		label: () => t`Paste the incoming webhook URL`,
		placeholder: "https://chat.example.com/hooks/…/…",
		extract(input) {
			const u = parseGoURL(input.trim())
			if (!u || (u.scheme !== "https" && u.scheme !== "http")) return null
			const m = /^\/hooks\/([^/]+)\/([^/]+)\/?$/.exec(u.path)
			return m ? { host: u.host, tokenA: m[1], tokenB: m[2] } : null
		},
	},
	base: (f) => {
		const ch = str(f.channel)
		// Shoutrrr prefixes channels with "#" itself; "@user" is kept as is
		const seg = ch.startsWith("@") ? ch : ch.replace(/^#/, "")
		return {
			url: `rocketchat://${authority(str(f.username), null, str(f.host))}/${encSegment(str(f.tokenA))}/${encSegment(str(f.tokenB))}${seg ? `/${encSegment(seg)}` : ""}`,
		}
	},
	parseBase(u) {
		if (u.user?.password != null) return null
		const segs = u.path.split("/")
		if (segs.length > 4 || (u.fragment !== null && segs.length < 4)) return null
		let channel = segs[3] ?? ""
		if (u.fragment) channel = `#${u.fragment}`
		else if (channel && !channel.startsWith("@")) channel = `#${channel}`
		return {
			fields: { host: u.host, username: u.user?.username ?? "", tokenA: segs[1] ?? "", tokenB: segs[2] ?? "", channel },
		}
	},
	summary: (f) => (str(f.channel) ? `${str(f.host)} · ${str(f.channel)}` : str(f.host)),
})

const SIGNAL_RECIPIENT_RE = /^(\+?[0-9\s)(+-]+|group\.[a-zA-Z0-9_+/=-]+|u:.+)$/

const signal = defineService({
	id: "signal",
	name: "Signal",
	schemes: ["signal"],
	fields: [
		hostField({
			defaultValue: "localhost",
			placeholder: "localhost",
			help: () => t`Host of your signal-cli-rest-api server.`,
		}),
		{ key: "port", label: () => t`Port`, type: "number", defaultValue: "8080", placeholder: "8080", validate: vPort },
		{
			key: "source",
			label: () => t`Sender number`,
			type: "text",
			required: true,
			placeholder: "+15551234567",
			validate: vPhone,
		},
		{
			key: "recipients",
			label: () => t`Recipients`,
			type: "list",
			required: true,
			placeholder: "+15557654321, group.abc…",
			help: () => t`Phone numbers, group.<id> group IDs or u:username.`,
			validate: (v) =>
				list(v).every((r) => SIGNAL_RECIPIENT_RE.test(r))
					? undefined
					: t`Each recipient must be a phone number, group.<id> or u:username`,
		},
		{ key: "token", label: () => t`API token`, type: "password", query: "token", aliases: ["apikey"], advanced: true },
		{ key: "user", label: () => t`Basic auth username`, type: "text", advanced: true },
		{ key: "password", label: () => t`Basic auth password`, type: "password", advanced: true },
		disableTlsField(),
	],
	base: (f) => {
		const port = str(f.port)
		// recipients are path segments; group IDs may contain "/" which Shoutrrr re-joins
		const recipients = list(f.recipients).map((r) => r.split("/").map(encSegment).join("/"))
		return {
			url: `signal://${authority(str(f.user), optional(str(f.password)), str(f.host) + (port ? `:${port}` : ""))}/${encSegment(str(f.source))}/${recipients.join("/")}`,
		}
	},
	parseBase(u) {
		const parts = u.path.replace(/^\/+|\/+$/g, "").split("/")
		const recipients: string[] = []
		for (const part of parts.slice(1)) {
			const last = recipients[recipients.length - 1]
			if (last?.startsWith("group.") && !part.startsWith("+") && !part.startsWith("group.") && !part.startsWith("u:"))
				recipients[recipients.length - 1] = `${last}/${part}`
			else recipients.push(part)
		}
		return {
			fields: {
				host: u.hostname,
				port: u.port || "8080",
				source: parts[0] ?? "",
				recipients: recipients.filter(Boolean),
				user: u.user?.username ?? "",
				password: u.user?.password ?? "",
			},
		}
	},
	summary: (f) => list(f.recipients).map(maskPhone).join(", "),
	check: (f) => (str(f.password) && !str(f.user) ? { password: t`A password requires a username` } : {}),
})

function maskPhone(p: string): string {
	return p.length > 6 && /^\+?\d/.test(p) ? `${p.slice(0, 3)}…${p.slice(-2)}` : maskSecret(p)
}

const signalgrid = defineService({
	id: "signalgrid",
	name: "Signalgrid",
	schemes: ["signalgrid"],
	fields: [
		{ key: "clientKey", label: () => t`Client key`, type: "password", required: true },
		{ key: "channel", label: () => t`Channel token`, type: "text", required: true, validate: vHostToken },
		{
			key: "type",
			label: () => t`Type`,
			type: "select",
			query: "type",
			defaultValue: "INFO",
			fromQuery: (v) => v.toUpperCase(),
			options: ["CRIT", "WARN", "INFO", "SUCCESS"].map((v) => ({ value: v, label: () => v })),
		},
		{
			key: "critical",
			label: () => t`Deliver as critical alert`,
			type: "boolean",
			query: "critical",
			defaultValue: false,
		},
	],
	base: (f) => ({ url: `signalgrid://${authority(str(f.clientKey), null, str(f.channel))}` }),
	parseBase(u) {
		if (u.user?.password != null || u.port || (u.path !== "" && u.path !== "/")) return null
		return { fields: { clientKey: u.user?.username ?? "", channel: u.hostname } }
	},
	summary: (f) => t`channel ${maskSecret(str(f.channel))}`,
})

const slack = defineService({
	id: "slack",
	name: "Slack",
	schemes: ["slack"],
	fields: [
		{
			key: "mode",
			label: () => t`Connection`,
			type: "select",
			defaultValue: "webhook",
			options: [
				{ value: "webhook", label: () => t`Incoming webhook` },
				{ value: "bot", label: () => t`Bot token (xoxb)` },
			],
		},
		{
			key: "token",
			label: () => t`Webhook URL or token`,
			type: "password",
			required: true,
			placeholder: "https://hooks.slack.com/services/T…/B…/…",
			help: () => t`An incoming webhook URL, or a bot token like xoxb-… for bot mode.`,
			validate: (v, f) => {
				const tok = parseSlackToken(str(v))
				if (!tok) return t`Not a valid Slack webhook URL or token`
				if ((tok.type === "hook") !== (str(f.mode) !== "bot"))
					return str(f.mode) === "bot" ? t`Enter a bot token (xoxb-…)` : t`Enter an incoming webhook URL`
				return undefined
			},
		},
		{
			key: "channel",
			label: () => t`Channel ID`,
			type: "text",
			required: true,
			placeholder: "C001CH4NN3L",
			visible: (f) => str(f.mode) === "bot",
			validate: vHostToken,
		},
		{ key: "botname", label: () => t`Bot name`, type: "text", query: "botname", aliases: ["username"] },
		{
			key: "icon",
			label: () => t`Icon`,
			type: "text",
			query: "icon",
			aliases: ["icon_emoji", "icon_url"],
			help: () => t`Emoji name or image URL`,
			advanced: true,
		},
		{
			key: "color",
			label: () => t`Border color`,
			type: "text",
			query: "color",
			placeholder: "good, #ff0000",
			advanced: true,
		},
		{ key: "threadTs", label: () => t`Thread timestamp`, type: "text", query: "thread_ts", advanced: true },
	],
	paste: {
		label: () => t`Paste the Slack webhook URL`,
		placeholder: "https://hooks.slack.com/services/…",
		extract: (input) => (parseSlackWebhookUrl(input) ? { mode: "webhook", token: input.trim() } : null),
	},
	base(f) {
		const tok = parseSlackToken(str(f.token))
		const cred = tok ? `${tok.type}:${tok.parts.join("-")}` : ":"
		const host = str(f.mode) === "bot" ? str(f.channel) : "webhook"
		return { url: `slack://${cred}@${host}` }
	},
	parseBase(u) {
		let raw: string
		const fields: FieldValues = {}
		if (u.path.length > 1) {
			// legacy form: slack://[botname@]token-a/token-b/token-c
			raw = u.hostname + u.path
			if (u.user?.password != null) return null
			if (u.user?.username) fields.botname = u.user.username
		} else {
			if (!u.user) return null
			raw = u.user.password === null ? u.user.username : `${u.user.username}:${u.user.password}`
		}
		const tok = parseSlackToken(raw)
		if (!tok) return null
		if (tok.type === "hook") {
			// Shoutrrr only builds webhook URLs with the "webhook" placeholder host
			if (u.path.length <= 1 && u.hostname !== "webhook") return null
			fields.mode = "webhook"
			fields.token = `https://hooks.slack.com/services/${tok.parts.join("/")}`
		} else {
			if (u.path.length > 1) return null
			fields.mode = "bot"
			fields.token = `${tok.type}-${tok.parts.join("-")}`
			fields.channel = u.hostname
		}
		return { fields }
	},
	summary: (f) => (str(f.mode) === "bot" ? t`bot · ${str(f.channel)}` : t`webhook`),
	secretParts: (f) => {
		const tok = parseSlackToken(str(f.token))
		return tok ? [tok.parts[2]] : []
	},
})

const TEAMS_WORKFLOW_RE =
	/^https:\/\/[a-zA-Z0-9][a-zA-Z0-9.-]*\.logic\.azure(?:\.[a-z]{2,})?(:\d+)?\/(?:powerautomate\/automations\/direct\/(?:[A-Za-z0-9-]+\/)*)?workflows\/|^https:\/\/[a-zA-Z0-9][a-zA-Z0-9.-]*\.environment\.api\.powerplatform\.com(:\d+)?\/powerautomate\/automations\/direct\/(?:[A-Za-z0-9-]+\/)*workflows\//

const teams = defineService({
	id: "teams",
	name: "Microsoft Teams",
	schemes: ["teams"],
	fields: [
		{
			key: "webhookUrl",
			label: () => t`Workflow webhook URL`,
			type: "password",
			query: "host",
			required: true,
			placeholder: "https://….logic.azure.com/workflows/…",
			help: () =>
				t`Create a "Post to a channel when a webhook request is received" workflow in Teams and paste its URL. Legacy Office 365 connector URLs are no longer supported.`,
			validate: (v) => (TEAMS_WORKFLOW_RE.test(str(v)) ? undefined : t`Must be a Power Automate workflow webhook URL`),
		},
		{
			key: "color",
			label: () => t`Title color`,
			type: "select",
			query: "color",
			defaultValue: "",
			advanced: true,
			options: [
				{ value: "", label: () => t`Default` },
				...["accent", "good", "warning", "attention", "dark", "light"].map((c) => ({ value: c, label: () => c })),
			],
		},
	],
	base: () => ({ url: "teams://" }),
	parseBase(u) {
		// the legacy teams://group@tenant/... format is rejected by Shoutrrr v0.21
		if (u.user || u.host || (u.path !== "" && u.path !== "/")) return null
		return { fields: {} }
	},
	summary: (f) => parseGoURL(str(f.webhookUrl))?.hostname ?? "",
})

const telegram = defineService({
	id: "telegram",
	name: "Telegram",
	schemes: ["telegram"],
	fields: [
		{
			key: "token",
			label: () => t`Bot token`,
			type: "password",
			required: true,
			placeholder: "123456789:AAE…",
			validate: (v) =>
				/^[0-9]+:[a-zA-Z0-9_-]+$/.test(str(v)) ? undefined : t`Expected a token like 123456789:ABC-def…`,
		},
		{
			key: "chats",
			label: () => t`Chats`,
			type: "list",
			query: "chats",
			aliases: ["channels"],
			required: true,
			placeholder: "@channel-name, -1001234567890",
			help: () => t`Chat IDs or @channel names.`,
		},
		{
			key: "parseMode",
			label: () => t`Parse mode`,
			type: "select",
			query: "parsemode",
			defaultValue: "None",
			advanced: true,
			fromQuery: (v) =>
				["None", "Markdown", "HTML", "MarkdownV2"].find((m) => m.toLowerCase() === v.toLowerCase()) ?? null,
			options: ["None", "Markdown", "HTML", "MarkdownV2"].map((m) => ({ value: m, label: () => m })),
		},
		{
			key: "notification",
			label: () => t`Play notification sound`,
			type: "boolean",
			query: "notification",
			defaultValue: true,
		},
		{
			key: "preview",
			label: () => t`Show link previews`,
			type: "boolean",
			query: "preview",
			defaultValue: true,
			advanced: true,
		},
	],
	base(f) {
		const tok = str(f.token)
		const idx = tok.indexOf(":")
		const user = idx === -1 ? tok : tok.slice(0, idx)
		const pass = idx === -1 ? "" : tok.slice(idx + 1)
		return { url: `telegram://${encUser(user)}:${encUser(pass)}@telegram` }
	},
	parseBase(u) {
		if (!u.user || u.host !== "telegram") return null
		return { fields: { token: u.user.password === null ? u.user.username : `${u.user.username}:${u.user.password}` } }
	},
	summary: (f) => list(f.chats).join(", "),
	secretParts: (f) => [str(f.token).split(":")[1] ?? ""],
})

const normalizePhone = (s: string) => (s.startsWith("MG") ? s : s.replace(/[\s\-().]/g, ""))

const twilio = defineService({
	id: "twilio",
	name: "Twilio",
	schemes: ["twilio"],
	fields: [
		{ key: "accountSid", label: () => t`Account SID`, type: "text", required: true, placeholder: "AC…" },
		{ key: "authToken", label: () => t`Auth token`, type: "password", required: true },
		{
			key: "from",
			label: () => t`From number`,
			type: "text",
			required: true,
			placeholder: "+15551234567",
			help: () => t`Phone number or Messaging Service SID (MG…).`,
			validate: (v) => (str(v).startsWith("MG") ? vHostToken(v) : vPhone(v)),
		},
		{
			key: "to",
			label: () => t`To numbers`,
			type: "list",
			required: true,
			placeholder: "+15557654321",
			validate: (v) => list(v).map(vPhone).find(Boolean),
		},
	],
	base: (f) => ({
		url: `twilio://${authority(str(f.accountSid), str(f.authToken), normalizePhone(str(f.from)))}/${list(f.to)
			.map((n) => encSegment(normalizePhone(n)))
			.join("/")}`,
	}),
	parseBase(u) {
		if (!u.user || u.port) return null
		return {
			fields: {
				accountSid: u.user.username,
				authToken: u.user.password ?? "",
				from: u.host,
				to: u.path.split("/").filter(Boolean),
			},
		}
	},
	check: (f) =>
		!str(f.from).startsWith("MG") &&
		list(f.to)
			.map(normalizePhone)
			.includes(normalizePhone(str(f.from)))
			? { to: t`Recipients can't include the sender number` }
			: {},
	summary: (f) => list(f.to).map(maskPhone).join(", "),
})

const wecom = defineService({
	id: "wecom",
	name: "WeCom",
	schemes: ["wecom"],
	fields: [
		{
			key: "key",
			label: () => t`Bot webhook key`,
			type: "password",
			required: true,
			validate: (v) =>
				/[@!#$%^&*()+=[\]{}|\\:;"'<>?,./\s]/.test(str(v))
					? t`Contains characters that are not allowed here (/ ? # @ : or spaces)`
					: undefined,
		},
		{ key: "mentioned", label: () => t`Mention users`, type: "text", query: "mentioned_list", advanced: true },
		{
			key: "mentionedMobile",
			label: () => t`Mention mobile numbers`,
			type: "text",
			query: "mentioned_mobile_list",
			advanced: true,
		},
	],
	paste: {
		label: () => t`Paste the bot webhook URL`,
		placeholder: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=…",
		extract(input) {
			const u = parseGoURL(input.trim())
			if (!u || u.hostname !== "qyapi.weixin.qq.com") return null
			const key = u.query.find(([k]) => k === "key")?.[1]
			return key ? { key } : null
		},
	},
	base: (f) => ({ url: `wecom://${str(f.key)}` }),
	parseBase(u) {
		if (u.user || u.port || (u.path !== "" && u.path !== "/")) return null
		return { fields: { key: u.host } }
	},
	summary: (f) => t`key ${maskSecret(str(f.key))}`,
})

const zulip = defineService({
	id: "zulip",
	name: "Zulip",
	schemes: ["zulip"],
	caseSensitive: true,
	fields: [
		hostField({ placeholder: "example.zulipchat.com" }),
		{
			key: "botMail",
			label: () => t`Bot email`,
			type: "text",
			required: true,
			placeholder: "alerts-bot@example.zulipchat.com",
		},
		{ key: "botKey", label: () => t`Bot API key`, type: "password", required: true },
		{
			key: "type",
			label: () => t`Message type`,
			type: "select",
			query: "type",
			defaultValue: "",
			options: [
				{ value: "", label: () => t`Channel (stream)` },
				{ value: "direct", label: () => t`Direct message` },
			],
		},
		{
			key: "stream",
			label: () => t`Stream`,
			type: "text",
			query: "stream",
			placeholder: "alerts",
			visible: (f) => str(f.type) !== "direct",
		},
		{ key: "topic", label: () => t`Topic`, type: "text", query: "topic", visible: (f) => str(f.type) !== "direct" },
		{
			key: "to",
			label: () => t`Recipients`,
			type: "text",
			query: "to",
			required: true,
			placeholder: "jane@example.com, 42",
			visible: (f) => str(f.type) === "direct",
		},
	],
	base: (f) => ({ url: `zulip://${authority(str(f.botMail), str(f.botKey), str(f.host))}` }),
	parseBase(u) {
		if (u.path !== "" && u.path !== "/") return null
		// "channel" is the explicit form of the default
		const rest = u.query.filter(([k, v]) => !(k === "type" && v === "channel"))
		if (u.query.some(([k, v]) => k === "type" && v !== "channel" && v !== "direct")) return null
		return { fields: { host: u.host, botMail: u.user?.username ?? "", botKey: u.user?.password ?? "" }, rest }
	},
	summary: (f) => (str(f.type) === "direct" ? `${str(f.host)} · ${str(f.to)}` : `${str(f.host)} · ${str(f.stream)}`),
})

// ---------------------------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------------------------

export const services: ShoutrrrService[] = [
	generic,
	bark,
	discord,
	googlechat,
	gotify,
	ifttt,
	join,
	lark,
	matrix,
	mattermost,
	mqtt,
	ntfy,
	opsgenie,
	pushbullet,
	pushover,
	rocketchat,
	signal,
	signalgrid,
	slack,
	teams,
	telegram,
	twilio,
	wecom,
	zulip,
]

export function getService(id: string): ShoutrrrService | undefined {
	return services.find((s) => s.id === id)
}

/** Finds the service for a Shoutrrr URL and parses it into form values; null for anything the builder can't represent. */
export function detectService(url: string): { service: ShoutrrrService; values: ServiceValues } | null {
	const u = parseGoURL(url)
	if (!u) return null
	const service = services.find((s) => s.schemes.includes(u.scheme))
	if (!service) return null
	const values = service.parse(url)
	return values ? { service, values } : null
}

/** Replaces secrets in a URL (in any of the encodings the builder uses) with a short masked form. */
export function maskUrl(url: string, secrets: string[]): string {
	let out = url
	const sorted = [...new Set(secrets)].filter((s) => s.length > 0).sort((a, b) => b.length - a.length)
	for (const secret of sorted) {
		const masked = secret.length <= 6 ? "••••" : `${secret.slice(0, 3)}••••`
		for (const form of new Set([secret, encUser(secret), encSegment(secret), encQuery(secret)])) {
			out = out.split(form).join(masked)
		}
	}
	return out
}

/** Masks a URL we can't parse into fields: hides userinfo password, path and query values. */
export function maskUnknownUrl(url: string): string {
	const u = parseGoURL(url)
	if (!u) return url.length > 12 ? `${url.slice(0, 12)}…` : url
	const hasUser = u.user !== null
	const path = u.rawPath.length > 1 ? "/…" : ""
	return `${u.scheme}://${hasUser ? "••••@" : ""}${u.host}${path}${u.rawQuery ? "?…" : ""}`
}
