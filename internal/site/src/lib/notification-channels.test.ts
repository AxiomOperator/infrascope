import { expect, test } from "bun:test"
import {
	channelPayload,
	defaultSeverity,
	draftFromChannel,
	effectiveSeverity,
	hasLegacyDestinations,
	legacyChannelDrafts,
	newChannelDraft,
	normalizeTemplate,
	routeAlert,
	severityRank,
	sortChannels,
	TEMPLATE_MAX_CHARS,
	templateTooLong,
	validateChannelDraft,
} from "./notification-channels"

const ch = (
	id: string,
	minSeverity: "info" | "warning" | "critical",
	opts: { enabled?: boolean; isDefault?: boolean } = {}
) => ({
	id,
	minSeverity,
	enabled: opts.enabled ?? true,
	isDefault: opts.isDefault ?? true,
})

test("severity ranks", () => {
	expect(severityRank("info")).toBe(0)
	expect(severityRank("warning")).toBe(1)
	expect(severityRank("critical")).toBe(2)
	expect(severityRank("")).toBe(0)
})

test("default severities by alert type", () => {
	for (const name of ["Status", "MonitorDown", "ContainerHealth", "SystemdFailed"]) {
		expect(defaultSeverity(name)).toBe("critical")
	}
	for (const name of ["CPU", "Memory", "Disk", "MonitorLoss", "MonitorLatency", "NetworkMonitorLoss", "Battery"]) {
		expect(defaultSeverity(name)).toBe("warning")
	}
	expect(defaultSeverity("MonitorCert", 10)).toBe("warning")
	expect(defaultSeverity("MonitorCert", 3)).toBe("critical")
	expect(defaultSeverity("MonitorCert", -1)).toBe("critical")
	expect(effectiveSeverity({ name: "CPU", severity: "info" })).toBe("info")
	expect(effectiveSeverity({ name: "CPU", severity: "" })).toBe("warning")
	expect(effectiveSeverity({ name: "MonitorCert", val: 2 })).toBe("critical")
})

test("routing matrix", () => {
	const channels = [
		ch("all", "info"),
		ch("warn", "warning"),
		ch("crit", "critical"),
		ch("off", "info", { enabled: false }),
		ch("manual", "info", { isDefault: false }),
	]
	const ids = (list: { id: string }[]) => list.map((c) => c.id)
	expect(ids(routeAlert(channels, "info"))).toEqual(["all"])
	expect(ids(routeAlert(channels, "warning"))).toEqual(["all", "warn"])
	expect(ids(routeAlert(channels, "critical"))).toEqual(["all", "warn", "crit"])
	// explicit channels ignore thresholds and the default flag, but not the enabled switch
	expect(ids(routeAlert(channels, "info", ["manual", "crit", "off"]))).toEqual(["crit", "manual"])
	// unknown explicit channels fall back to default routing
	expect(ids(routeAlert(channels, "warning", ["deleted"]))).toEqual(["all", "warn"])
})

test("legacy settings become default channels", () => {
	const drafts = legacyChannelDrafts(
		{ emails: ["a@example.com", " "], webhooks: ["ntfy://ntfy.sh/a", "ntfy://ntfy.sh/b", "generic://x"] },
		(url) => (url.startsWith("ntfy") ? "ntfy" : "")
	)
	expect(drafts.map((d) => [d.name, d.type])).toEqual([
		["Email", "email"],
		["ntfy", "shoutrrr"],
		["ntfy 2", "shoutrrr"],
		["Webhook", "shoutrrr"],
	])
	expect(drafts[0].config).toEqual({ addresses: ["a@example.com"] })
	expect(drafts[1].config).toEqual({ url: "ntfy://ntfy.sh/a" })
	for (const d of drafts) {
		expect(d.enabled && d.isDefault && d.minSeverity === "info").toBe(true)
	}
	expect(legacyChannelDrafts({}, () => "")).toEqual([])
	expect(hasLegacyDestinations({ emails: [""], webhooks: [] })).toBe(false)
	expect(hasLegacyDestinations({ webhooks: ["x://y"] })).toBe(true)
})

test("templates", () => {
	expect(normalizeTemplate({ title: " ", body: "" })).toBeNull()
	expect(normalizeTemplate({ title: "{{.Title}}", body: "  " })).toEqual({ title: "{{.Title}}" })
	expect(templateTooLong({ body: "x".repeat(TEMPLATE_MAX_CHARS + 1) })).toEqual(["body"])
	expect(templateTooLong(null)).toEqual([])
})

test("channel drafts validate and serialize by type", () => {
	const email = newChannelDraft("email", "Ops")
	expect(validateChannelDraft(email)).toEqual({ config: true })
	email.config.addresses = ["ops@example.com"]
	expect(validateChannelDraft(email)).toEqual({})
	expect(channelPayload({ ...email, config: { ...email.config, url: "x" } }).config).toEqual({
		addresses: ["ops@example.com"],
	})
	const hook = newChannelDraft("shoutrrr", "")
	expect(validateChannelDraft(hook)).toEqual({ name: true, config: true })
	expect(validateChannelDraft({ ...hook, name: "n", config: { url: "ntfy://ntfy.sh/t" } })).toEqual({})
	expect(channelPayload(newChannelDraft("browser", " Phone ")).config).toEqual({})
	expect(channelPayload(newChannelDraft("browser", " Phone ")).name).toBe("Phone")
	const draft = draftFromChannel({
		id: "1",
		user: "u",
		name: "x",
		type: "shoutrrr",
		config: { url: "ntfy://a" },
		enabled: false,
		minSeverity: "bogus" as "info",
		isDefault: false,
		template: { title: "", body: "b" },
		created: "",
		updated: "",
		collectionId: "",
		collectionName: "",
	})
	expect(draft.minSeverity).toBe("info")
	expect(draft.template).toEqual({ body: "b" })
})

test("sortChannels puts defaults first", () => {
	const sorted = sortChannels([
		{ name: "b", isDefault: false },
		{ name: "z", isDefault: true },
		{ name: "a", isDefault: true },
	])
	expect(sorted.map((c) => c.name)).toEqual(["a", "z", "b"])
})
