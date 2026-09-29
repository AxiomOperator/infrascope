import { describe, expect, mock, test } from "bun:test"

// Lingui macros are compiled away by the SWC plugin in the app build; bun runs the source directly.
mock.module("@lingui/core/macro", () => ({
	t: (strings: TemplateStringsArray, ...values: unknown[]) => String.raw({ raw: strings }, ...values),
}))

const lib = await import("./shoutrrr")
const { detectService, getService, services } = lib

type Fields = Record<string, string | boolean | string[]>

// Fake Slack credentials, assembled at runtime so secret scanners don't flag them.
const SLACK_BOT_TOKEN = ["xoxb", "123456789012", "1234567890123", "4mt0t4l1YL3g1T5L4cK70k3N"].join("-")
const SLACK_HOOK = ["https://hooks.slack.com/services", "T00000000", "B00000000", "X".repeat(24)].join("/")
const SLACK_HOOK_2 = ["https://hooks.slack.com/services", "WNA3PBYV6", "F20DUQND3RQ", "Webc4MAvoacrpPakR8phF0zi"].join("/")

const svc = (id: string) => {
	const s = getService(id)
	if (!s) throw new Error(`unknown service ${id}`)
	return s
}

const build = (id: string, fields: Fields, extra: [string, string][] = []) => svc(id).build({ fields, extra })

/** Sample values for every service; built URL must parse back to exactly these values. */
const samples: Record<string, Fields> = {
	generic: {
		url: "https://example.com/api/hook?token=abc&contenttype=text",
		template: "json",
		titleKey: "subject",
		method: "PUT",
		headers: ["Authorization=Bearer x=y", "X-Extra=a, b"],
		data: ["source=infrascope"],
	},
	bark: { deviceKey: "dev/ice:key", host: "bark.example.com:8080", path: "/push", sound: "alarm", scheme: "http" },
	discord: {
		webhookId: "693853386302554172",
		token: "W3dE2OZz-4C_13",
		username: "Infra Bot",
		splitLines: false,
		raw: true,
	},
	googlechat: { path: "/v1/spaces/AAAA/messages", key: "k/e+y", token: "tok=en" },
	gotify: { host: "gotify.example.com:8443", path: "sub/path", token: "AbCdEf123", priority: "5", disableTls: true },
	ifttt: { webhookKey: "abc_DEF-123", events: ["e1", "e2"], value1: "v 1", titleValue: "1", messageValue: "3" },
	join: { apiKey: "api:key@1", devices: ["d1", "d2"], icon: "https://example.com/i.png" },
	lark: { host: "open.feishu.cn", token: "tok-123", secret: "s3cr#t" },
	matrix: {
		host: "matrix.example.com:8448",
		user: "bot",
		password: "p@ss:w/rd",
		rooms: ["!room:example.com", "#alias:example.com"],
	},
	mattermost: { host: "mm.example.com", token: "tok123", channel: "alerts", username: "infra", disableTls: true },
	mqtt: {
		host: "broker.local",
		port: "8883",
		tls: true,
		topic: "home/alerts",
		username: "user",
		password: "p#ss",
		qos: "1",
		retained: true,
	},
	ntfy: { host: "ntfy.sh", topic: "alerts", password: "tk_abc", priority: "5", tags: ["warning", "skull"] },
	opsgenie: {
		host: "api.eu.opsgenie.com",
		apiKey: "key-123",
		responders: ["team:ops", "user:jane@example.com"],
		priority: "P2",
	},
	pushbullet: {
		token: "o.abcdefghijklmnopqrstuvwxyz1234567"?.slice(0, 34),
		targets: ["phone", "#channel", "me@example.com"],
	},
	pushover: {
		userKey: "uQiRzpo4DXghDmr9QzzfQu27cmVRsG",
		token: "azGDORePK8gMaC0QOYAMyEEuzJnyUi",
		devices: ["d1"],
		priority: "1",
	},
	rocketchat: { host: "chat.example.com:3000", tokenA: "tokA", tokenB: "tokB", channel: "#general", username: "bot" },
	signal: {
		host: "localhost",
		port: "8080",
		source: "+1234567890",
		recipients: ["+0987654321", "group.abc+/=", "u:jane.01"],
		token: "tok en",
	},
	signalgrid: { clientKey: "client key", channel: "chan123", type: "CRIT", critical: true },
	slack: {
		mode: "bot",
		token: SLACK_BOT_TOKEN,
		channel: "C001CH4NN3L",
		botname: "Infra",
		color: "good",
	},
	teams: {
		webhookUrl:
			"https://prod-00.westus.logic.azure.com:443/workflows/abc/triggers/manual/paths/invoke?api-version=2016-06-01&sp=%2Ftriggers%2Fmanual%2Frun&sig=x%2By",
		color: "attention",
	},
	telegram: {
		token: "110201543:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw",
		chats: ["@channel-1", "-1001234"],
		parseMode: "HTML",
		notification: false,
	},
	twilio: { accountSid: "AC123", authToken: "tok/en", from: "+15551234567", to: ["+15557654321", "+15550000000"] },
	wecom: { key: "693axxx6-7aoc-4bc4-97a0-0ec2sifa5aaa", mentioned: "@all" },
	zulip: {
		host: "example.zulipchat.com",
		botMail: "bot@example.zulipchat.com",
		botKey: "k:e/y",
		stream: "alerts",
		topic: "infra stuff",
	},
}

describe("build → parse round-trip", () => {
	test("every service has a sample", () => {
		expect(Object.keys(samples).sort()).toEqual(services.map((s) => s.id).sort())
		expect(services).toHaveLength(24)
	})

	for (const service of services) {
		test(service.id, () => {
			const fields = samples[service.id]
			const url = service.build({ fields, extra: [] })
			expect(service.validate({ fields, extra: [] })).toEqual({})
			expect(() => new URL(url)).not.toThrow()
			const parsed = service.parse(url)
			expect(parsed).not.toBeNull()
			expect(parsed?.extra).toEqual([])
			expect(parsed?.fields).toEqual({ ...service.defaults().fields, ...fields })
			// idempotent
			expect(service.build(parsed as NonNullable<typeof parsed>)).toBe(url)
			// detectService picks the same service
			expect(detectService(url)?.service.id).toBe(service.id)
		})
	}
})

describe("generated URLs", () => {
	test("exact output for common services", () => {
		expect(build("discord", { webhookId: "123", token: "abc" })).toBe("discord://abc@123")
		expect(build("ntfy", { host: "ntfy.sh", topic: "alerts", password: "tk" })).toBe("ntfy://:tk@ntfy.sh/alerts")
		expect(build("bark", { deviceKey: "key", host: "api.day.app" })).toBe("bark://:key@api.day.app/")
		expect(build("gotify", { host: "gotify.example.com", token: "AbC" })).toBe("gotify://gotify.example.com/AbC")
		expect(build("telegram", { token: "123:ABC", chats: ["@chan", "42"] })).toBe(
			"telegram://123:ABC@telegram?chats=@chan,42"
		)
		expect(build("pushover", { userKey: "user", token: "tok", devices: ["a", "b"] })).toBe(
			"pushover://shoutrrr:tok@user/?devices=a,b"
		)
		expect(build("join", { apiKey: "key", devices: ["d1"] })).toBe("join://shoutrrr:key@join/?devices=d1")
		expect(build("signal", { source: "+1234567890", recipients: ["+0987654321", "+1123456789"] })).toBe(
			"signal://localhost:8080/+1234567890/+0987654321/+1123456789"
		)
		expect(build("pushbullet", { token: "t".repeat(34), targets: ["dev", "#chan"] })).toBe(
			`pushbullet://${"t".repeat(34)}/dev/%23chan`
		)
		expect(build("rocketchat", { host: "chat.example.com", tokenA: "a", tokenB: "b", channel: "#general" })).toBe(
			"rocketchat://chat.example.com/a/b/general"
		)
		expect(build("rocketchat", { host: "chat.example.com", tokenA: "a", tokenB: "b", channel: "@jane" })).toBe(
			"rocketchat://chat.example.com/a/b/@jane"
		)
		expect(
			build("slack", {
				mode: "webhook",
				token: SLACK_HOOK,
			})
		).toBe("slack://hook:T00000000-B00000000-XXXXXXXXXXXXXXXXXXXXXXXX@webhook")
		expect(build("teams", { webhookUrl: "https://x.logic.azure.com/workflows/1?a=b&c=d" })).toBe(
			"teams://?host=https://x.logic.azure.com/workflows/1%3Fa%3Db%26c%3Dd"
		)
		expect(build("mqtt", { host: "broker", topic: "a/b" })).toBe("mqtt://broker/a/b")
		expect(build("generic", { url: "http://example.com/hook", template: "json" })).toBe(
			"generic://example.com/hook?disabletls=yes&template=json"
		)
	})

	test("default query values are omitted", () => {
		expect(build("ntfy", { host: "ntfy.sh", topic: "t", priority: "3", markdown: false })).toBe("ntfy://ntfy.sh/t")
		expect(build("discord", { webhookId: "1", token: "t", splitLines: true })).toBe("discord://t@1")
	})
})

describe("parsing guide examples", () => {
	const cases: [string, string, Fields][] = [
		["generic://example.com?template=json", "generic", { url: "https://example.com", template: "json" }],
		[
			"generic://example.com?template=json&titlekey=subject&messagekey=content",
			"generic",
			{ titleKey: "subject", messageKey: "content" },
		],
		["generic://example.com?@acceptLanguage=tlh-Piqd", "generic", { headers: ["acceptLanguage=tlh-Piqd"] }],
		[
			"generic://example.com/api/v1/postStuff?__contenttype=text/plain",
			"generic",
			{ url: "https://example.com/api/v1/postStuff?contenttype=text/plain", contentType: "application/json" },
		],
		["generic+http://example.com/hook", "generic", { url: "http://example.com/hook" }],
		["bark://:devicekey@host/", "bark", { deviceKey: "devicekey", host: "host" }],
		[
			"discord://W3dE2OZz4C13_4z_uHfDOoC7BqTW288s-z1ykqI0iJnY_HjRqMGO8Sc7YDqvf_KVKjhJ@693853386302554172",
			"discord",
			{
				webhookId: "693853386302554172",
				token: "W3dE2OZz4C13_4z_uHfDOoC7BqTW288s-z1ykqI0iJnY_HjRqMGO8Sc7YDqvf_KVKjhJ",
			},
		],
		["gotify://gotify.example.com/Aaa.bbb.ccc", "gotify", { host: "gotify.example.com", token: "Aaa.bbb.ccc" }],
		["gotify://example.com/gotify/Atoken/", "gotify", { path: "gotify", token: "Atoken" }],
		[
			"googlechat://chat.googleapis.com/v1/spaces/FOO/messages?key=bar&token=baz",
			"googlechat",
			{ path: "/v1/spaces/FOO/messages", key: "bar", token: "baz" },
		],
		["hangouts://chat.googleapis.com/v1/spaces/FOO/messages?key=bar&token=baz", "googlechat", { key: "bar" }],
		[
			"ifttt://key/?events=e1,e2&value1=a&value2=b&value3=c",
			"ifttt",
			{ webhookKey: "key", events: ["e1", "e2"], value1: "a", value2: "b", value3: "c" },
		],
		[
			"join://shoutrrr:api-key@join/?devices=d1,d2&icon=x",
			"join",
			{ apiKey: "api-key", devices: ["d1", "d2"], icon: "x" },
		],
		["lark://open.larksuite.com/token?secret=secret", "lark", { token: "token", secret: "secret" }],
		["mattermost://mm.example.com/token", "mattermost", { host: "mm.example.com", token: "token", channel: "" }],
		["mattermost://bot@mm.example.com/token/chan", "mattermost", { username: "bot", channel: "chan" }],
		[
			"matrix://username:password@host:8448/?rooms=!roomID1,roomAlias2",
			"matrix",
			{ user: "username", password: "password", host: "host:8448", rooms: ["!roomID1", "roomAlias2"] },
		],
		[
			"mqtts://user:pass@broker:8883/home/alerts",
			"mqtt",
			{ tls: true, host: "broker", port: "8883", topic: "home/alerts", username: "user", password: "pass" },
		],
		["mqtt://broker/topic", "mqtt", { tls: false, host: "broker", port: "", topic: "topic" }],
		["ntfy://:accesstoken@ntfy.sh/topic", "ntfy", { password: "accesstoken", username: "", topic: "topic" }],
		["ntfy://username:password@ntfy.example.com/topic?priority=high", "ntfy", { username: "username", priority: "4" }],
		[
			"opsgenie://api.opsgenie.com/token?responders=team:r1,user:r2",
			"opsgenie",
			{ apiKey: "token", responders: ["team:r1", "user:r2"] },
		],
		[
			`pushbullet://${"a".repeat(34)}/device/#channel`,
			"pushbullet",
			{ token: "a".repeat(34), targets: ["device", "#channel"] },
		],
		[`pushbullet://${"a".repeat(34)}`, "pushbullet", { targets: [] }],
		[
			"pushover://shoutrrr:apiToken@userKey/?devices=d1,d2",
			"pushover",
			{ token: "apiToken", userKey: "userKey", devices: ["d1", "d2"] },
		],
		["rocketchat://bot@chat.example.com/tokA/tokB/general", "rocketchat", { username: "bot", channel: "#general" }],
		["rocketchat://chat.example.com/tokA/tokB/@user", "rocketchat", { channel: "@user" }],
		["rocketchat://chat.example.com/tokA/tokB/x#general", "rocketchat", { tokenB: "tokB", channel: "#general" }],
		[
			"signal://localhost:8080/+1234567890/+0987654321/+1123456789/group.testgroup",
			"signal",
			{ source: "+1234567890", recipients: ["+0987654321", "+1123456789", "group.testgroup"] },
		],
		["signal://localhost:8080/+1234567890/+0987654321?token=YOUR_API_TOKEN", "signal", { token: "YOUR_API_TOKEN" }],
		["signal://user:pw@signal.local/+1234567890/+0987654321", "signal", { port: "8080", user: "user", password: "pw" }],
		["signalgrid://clientKey@channel", "signalgrid", { clientKey: "clientKey", channel: "channel" }],
		[
			"slack://hook:WNA3PBYV6-F20DUQND3RQ-Webc4MAvoacrpPakR8phF0zi@webhook?color=good&icon=man-scientist&botname=Shoutrrrbot",
			"slack",
			{
				mode: "webhook",
				token: SLACK_HOOK_2,
				color: "good",
				icon: "man-scientist",
				botname: "Shoutrrrbot",
			},
		],
		[
			`slack://xoxb:${SLACK_BOT_TOKEN.slice("xoxb-".length)}@C001CH4NN3L`,
			"slack",
			{ mode: "bot", token: SLACK_BOT_TOKEN, channel: "C001CH4NN3L" },
		],
		[
			"slack://Shoutrrrbot@WNA3PBYV6/F20DUQND3RQ/Webc4MAvoacrpPakR8phF0zi",
			"slack",
			{ mode: "webhook", botname: "Shoutrrrbot" },
		],
		[
			"teams://?host=https%3A%2F%2Fprod-00.westus.logic.azure.com%3A443%2Fworkflows%2Fabc",
			"teams",
			{ webhookUrl: "https://prod-00.westus.logic.azure.com:443/workflows/abc" },
		],
		[
			"telegram://123:ABC@telegram?chats=@channel-1,chat-id-1",
			"telegram",
			{ token: "123:ABC", chats: ["@channel-1", "chat-id-1"] },
		],
		["telegram://123:ABC@telegram?channels=@c", "telegram", { chats: ["@c"] }],
		[
			"twilio://ACsid:authToken@+15551234567/+15557654321",
			"twilio",
			{ accountSid: "ACsid", authToken: "authToken", from: "+15551234567", to: ["+15557654321"] },
		],
		["wecom://key", "wecom", { key: "key" }],
		[
			"zulip://bot@example.com:bot-key@zulip.example.com/?stream=alerts&topic=infra",
			"zulip",
			{ botMail: "bot@example.com", botKey: "bot-key", host: "zulip.example.com", stream: "alerts", topic: "infra" },
		],
		["zulip://bot%40example.com:key@z.example.com?type=direct&to=5", "zulip", { type: "direct", to: "5" }],
	]
	for (const [url, id, expected] of cases) {
		test(url, () => {
			const detected = detectService(url)
			expect(detected?.service.id).toBe(id)
			expect(detected?.values.fields).toMatchObject(expected)
			// rebuilding is stable
			const rebuilt = detected?.service.build(detected.values) as string
			expect(detected?.service.build(detected?.service.parse(rebuilt) as NonNullable<typeof detected>["values"])).toBe(
				rebuilt
			)
		})
	}
})

describe("unknown query parameters", () => {
	test("are preserved on edit", () => {
		const url = "ntfy://ntfy.sh/alerts?priority=high&title=Custom&Actions=view%2C+Open%2C+https%3A%2F%2Fx.y&future=1"
		const d = detectService(url)
		expect(d?.values.extra).toEqual([
			["title", "Custom"],
			["Actions", "view, Open, https://x.y"],
			["future", "1"],
		])
		const edited = { ...d?.values, fields: { ...d?.values.fields, topic: "other" } } as NonNullable<typeof d>["values"]
		const rebuilt = d?.service.build(edited) as string
		expect(rebuilt).toBe("ntfy://ntfy.sh/other?priority=4&title=Custom&Actions=view,%20Open,%20https://x.y&future=1")
		expect(detectService(rebuilt)?.values.extra).toEqual(d?.values.extra as [string, string][])
	})

	test("duplicate known keys keep the extra values verbatim", () => {
		const d = detectService("discord://t@1?username=a&username=b")
		expect(d?.values.fields.username).toBe("a")
		expect(d?.values.extra).toEqual([["username", "b"]])
	})

	test("generic keeps title and webhook query params", () => {
		const d = detectService("generic://example.com/hook?a=1&title=T&template=json&Template=x")
		expect(d?.values.fields.url).toBe("https://example.com/hook?a=1&Template=x")
		expect(d?.values.extra).toEqual([["title", "T"]])
		expect(d?.service.build(d.values)).toBe("generic://example.com/hook?a=1&Template=x&template=json&title=T")
	})

	test("query keys are matched case-insensitively for prop-resolver services", () => {
		expect(detectService("discord://t@1?SplitLines=no")?.values.fields.splitLines).toBe(false)
		expect(detectService("matrix://:tok@host?disableTLS=yes")?.values.fields.disableTls).toBe(true)
	})
})

describe("special characters in secrets", () => {
	const nasty = "p@ss/w:rd#?&=+ %ü💥"
	const cases: [string, string, Fields][] = [
		["ntfy", "password", { host: "ntfy.sh", topic: "t", username: "u", password: nasty }],
		["matrix", "password", { host: "m.org", password: nasty }],
		["zulip", "botKey", { host: "z.com", botMail: "bot@z.com", botKey: nasty }],
		["join", "apiKey", { apiKey: nasty, devices: ["d"] }],
		["twilio", "authToken", { accountSid: "AC1", authToken: nasty, from: "+1555", to: ["+1666"] }],
		["mqtt", "password", { host: "b", topic: "t", username: "u", password: nasty }],
		["googlechat", "token", { path: "/v1/spaces/A/messages", key: nasty, token: nasty }],
		["lark", "secret", { token: "t", secret: nasty }],
		["signal", "token", { source: "+1", recipients: ["+2"], token: nasty }],
		["gotify", "token", { host: "g.com", token: "a:b@c#d?e%f ü" }],
		["discord", "token", { webhookId: "1", token: "a:b@c#d?e%f ü" }],
	]
	for (const [id, key, fields] of cases) {
		test(`${id}.${key}`, () => {
			const url = build(id, fields)
			expect(() => new URL(url)).not.toThrow()
			// secrets never appear unescaped where they'd change the URL structure
			expect(url).not.toContain("#")
			expect(detectService(url)?.values.fields[key]).toBe(fields[key])
		})
	}

	test("userinfo is percent-encoded", () => {
		expect(build("ntfy", { host: "ntfy.sh", topic: "t", password: "a@b:c/d" })).toBe("ntfy://:a%40b%3Ac%2Fd@ntfy.sh/t")
		expect(build("zulip", { host: "z.com", botMail: "bot@z.com", botKey: "k" })).toBe("zulip://bot%40z.com:k@z.com")
	})

	test("query values with spaces and plus signs", () => {
		const url = build("ntfy", { host: "ntfy.sh", topic: "t", tags: ["a+b", "c d"] })
		expect(url).toBe("ntfy://ntfy.sh/t?tags=a%2Bb,c%20d")
		expect(detectService(url)?.values.fields.tags).toEqual(["a+b", "c d"])
		// Go decodes "+" in queries as a space
		expect(detectService("ntfy://ntfy.sh/t?tags=a+b")?.values.fields.tags).toEqual(["a b"])
	})
})

describe("detectService", () => {
	test("unknown or unsupported URLs return null", () => {
		expect(detectService("foo://bar")).toBeNull()
		expect(detectService("smtp://user:pass@host:587/?from=a@b.c&to=d@e.f")).toBeNull()
		expect(detectService("not a url")).toBeNull()
		expect(detectService("")).toBeNull()
		// legacy Teams connector format isn't supported by Shoutrrr v0.21
		expect(
			detectService("teams://11111111-4444@22222222-4444/33333333/44444444/V2ESyij?host=contoso.webhook.office.com")
		).toBeNull()
		// Shoutrrr reads the bark device key from the password only
		expect(detectService("bark://devicekey@host")).toBeNull()
		// discord only accepts an empty path or /raw
		expect(detectService("discord://t@1/other")).toBeNull()
		// invalid boolean values can't be represented
		expect(detectService("discord://t@1?splitlines=maybe")).toBeNull()
		// unknown select values
		expect(detectService("ntfy://ntfy.sh/t?priority=9")).toBeNull()
		// Shoutrrr ignores a rocketchat #fragment without a channel segment
		expect(detectService("rocketchat://chat.example.com/a/b#general")).toBeNull()
		// lark only supports the two official hosts
		expect(detectService("lark://example.com/token")).toBeNull()
	})

	test("scheme aliases", () => {
		expect(detectService("mqtts://b/t")?.service.id).toBe("mqtt")
		expect(detectService("hangouts://chat.googleapis.com/v1/spaces/A/messages?key=k&token=t")?.service.id).toBe(
			"googlechat"
		)
		const custom = detectService("generic+https://example.com/x")
		expect(custom).not.toBeNull()
		if (custom) expect(custom.service.build(custom.values)).toBe("generic://example.com/x")
	})
})

describe("paste helpers", () => {
	test("discord", () => {
		expect(lib.parseDiscordWebhookUrl("https://discord.com/api/webhooks/693853386302554172/W3dE2O-z_1")).toEqual({
			webhookId: "693853386302554172",
			token: "W3dE2O-z_1",
		})
		expect(lib.parseDiscordWebhookUrl(" https://discordapp.com/api/webhooks/1/tok/ ")).toEqual({
			webhookId: "1",
			token: "tok",
		})
		expect(lib.parseDiscordWebhookUrl("https://canary.discord.com/api/webhooks/1/tok")).toEqual({
			webhookId: "1",
			token: "tok",
		})
		expect(lib.parseDiscordWebhookUrl("https://example.com/api/webhooks/1/tok")).toBeNull()
		expect(lib.parseDiscordWebhookUrl("https://discord.com/api/webhooks/1")).toBeNull()
		const fields = svc("discord").paste?.extract("https://discord.com/api/webhooks/42/abc") as Fields
		expect(build("discord", fields)).toBe("discord://abc@42")
	})

	test("slack", () => {
		const hook = SLACK_HOOK
		expect(lib.parseSlackWebhookUrl(hook)).toBe("T00000000/B00000000/XXXXXXXXXXXXXXXXXXXXXXXX")
		expect(lib.parseSlackWebhookUrl("https://hooks.slack.com/workflows/T/A/1/x")).toBeNull()
		expect(lib.parseSlackWebhookUrl("https://example.com/services/T/B/X")).toBeNull()
		expect(lib.parseSlackToken(hook)).toEqual({
			type: "hook",
			parts: ["T00000000", "B00000000", "XXXXXXXXXXXXXXXXXXXXXXXX"],
		})
		expect(lib.parseSlackToken(SLACK_BOT_TOKEN)?.type).toBe("xoxb")
		expect(lib.parseSlackToken("T00000000-B00000000/XXXXXXXXXXXXXXXXXXXXXXXX")).toBeNull()
		const fields = svc("slack").paste?.extract(hook) as Fields
		expect(build("slack", fields)).toBe("slack://hook:T00000000-B00000000-XXXXXXXXXXXXXXXXXXXXXXXX@webhook")
	})

	test("other services", () => {
		expect(svc("mattermost").paste?.extract("http://mm.local:8065/hooks/abc123")).toEqual({
			host: "mm.local:8065",
			token: "abc123",
			disableTls: true,
		})
		expect(svc("rocketchat").paste?.extract("https://chat.example.com/hooks/AAA/BBB")).toEqual({
			host: "chat.example.com",
			tokenA: "AAA",
			tokenB: "BBB",
		})
		expect(
			svc("googlechat").paste?.extract("https://chat.googleapis.com/v1/spaces/S/messages?key=k&token=t%3D")
		).toEqual({
			host: "chat.googleapis.com",
			path: "/v1/spaces/S/messages",
			key: "k",
			token: "t=",
		})
		expect(svc("lark").paste?.extract("https://open.feishu.cn/open-apis/bot/v2/hook/abc")).toEqual({
			host: "open.feishu.cn",
			token: "abc",
		})
		expect(svc("wecom").paste?.extract("https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abc-123")).toEqual({
			key: "abc-123",
		})
	})
})

describe("validation", () => {
	test("required fields and formats", () => {
		expect(Object.keys(svc("discord").validate({ fields: {}, extra: [] })).sort()).toEqual(["token", "webhookId"])
		expect(svc("discord").validate({ fields: { webhookId: "abc", token: "t" }, extra: [] }).webhookId).toBeTruthy()
		expect(svc("mqtt").validate({ fields: { host: "b", topic: "t", port: "70000" }, extra: [] }).port).toBeTruthy()
		expect(
			svc("twilio").validate({ fields: { accountSid: "a", authToken: "b", from: "+1555", to: ["+1555"] }, extra: [] })
				.to
		).toBeTruthy()
		expect(svc("signal").validate({ fields: { source: "abc", recipients: ["+1"] }, extra: [] }).source).toBeTruthy()
		expect(
			svc("ifttt").validate({ fields: { webhookKey: "k", events: ["e"], titleValue: "2" }, extra: [] }).titleValue
		).toBeTruthy()
		expect(
			svc("teams").validate({ fields: { webhookUrl: "https://contoso.webhook.office.com/webhookb2/x" }, extra: [] })
				.webhookUrl
		).toBeTruthy()
		expect(
			svc("slack").validate({
				fields: {
					mode: "bot",
					token: SLACK_HOOK,
					channel: "C1",
				},
				extra: [],
			}).token
		).toBeTruthy()
		// hidden fields aren't validated
		expect(
			svc("slack").validate({
				fields: {
					mode: "webhook",
					token: SLACK_HOOK,
				},
				extra: [],
			})
		).toEqual({})
	})
})

describe("masking", () => {
	test("maskUrl hides secrets", () => {
		const s = svc("ntfy")
		const values = { fields: { host: "ntfy.sh", topic: "alerts", password: "tk_supersecret" }, extra: [] }
		const masked = lib.maskUrl(s.build(values), s.secrets(values))
		expect(masked).toBe("ntfy://:tk_••••@ntfy.sh/alerts")
		expect(masked).not.toContain("supersecret")
	})
	test("summaries don't leak secrets", () => {
		for (const service of services) {
			const values = { fields: samples[service.id], extra: [] }
			const summary = service.summary(values)
			for (const secret of service.secrets(values)) {
				if (secret.length > 6) expect(summary).not.toContain(secret)
			}
		}
	})
	test("maskUnknownUrl", () => {
		expect(lib.maskUnknownUrl("smtp://user:pass@mail.example.com:587/?from=a@b")).toBe(
			"smtp://••••@mail.example.com:587?…"
		)
	})
})
