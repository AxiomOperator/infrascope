import { expect, test } from "bun:test"
import {
	buildMonitorPayload,
	type CheckFormState,
	checkFormFromMonitor,
	checkPayloadFromForm,
	formatBulkMonitorLine,
	hasCustomCheckOptions,
	monitorReportsCert,
	parseBulkMonitorLine,
} from "./monitor-form-utils"

const base = { system: "s1", target: "db.local", port: 0, server: "", interval: "30" }

test("port protocols get default ports", () => {
	expect(buildMonitorPayload({ ...base, protocol: "postgres" }).port).toBe(5432)
	expect(buildMonitorPayload({ ...base, protocol: "redis", port: 6380 }).port).toBe(6380)
	expect(buildMonitorPayload({ ...base, protocol: "tcp" }).port).toBe(443)
	expect(buildMonitorPayload({ ...base, protocol: "smtp" }).port).toBe(25)
	expect(buildMonitorPayload({ ...base, protocol: "smtp", check: { startTLS: true } }).port).toBe(587)
	expect(buildMonitorPayload({ ...base, protocol: "imap", check: { tls: true } }).port).toBe(993)
	expect(buildMonitorPayload({ ...base, protocol: "icmp", port: 22 }).port).toBe(0)
	expect(() => buildMonitorPayload({ ...base, protocol: "grpc" })).toThrow("Port")
})

test("docker targets must be container names", () => {
	expect(buildMonitorPayload({ ...base, target: "web_1", protocol: "docker" }).target).toBe("web_1")
	expect(() => buildMonitorPayload({ ...base, target: "../x", protocol: "docker" })).toThrow("container")
})

test("bulk lines round trip new protocols", () => {
	const payload = parseBulkMonitorLine("db.local,postgres", 1, "s1")
	expect(payload.protocol).toBe("postgres")
	expect(payload.port).toBe(5432)
	expect(formatBulkMonitorLine({ ...payload, check: null })).toBe("db.local,postgres")
	expect(formatBulkMonitorLine({ ...parseBulkMonitorLine("mc.local,minecraft,25566", 1, "s1"), check: null })).toBe(
		"mc.local,minecraft,25566"
	)
	expect(() => parseBulkMonitorLine("x,ftp", 1, "s1")).toThrow()
})

const emptyForm = checkFormFromMonitor()

test("check payload keeps only fields the protocol uses", () => {
	const form: CheckFormState = {
		recordType: "MX",
		expected: " mail.example.com ",
		matchMode: "equals",
		banner: "SSH-2.0-OpenSSH",
		tlsMode: "starttls",
		ignoreTLS: true,
		service: " health ",
		username: "app",
		password: "secret",
	}
	expect(checkPayloadFromForm("dns", form)).toEqual({
		check: { recordType: "MX", expected: "mail.example.com", matchMode: "equals" },
		secrets: null,
	})
	expect(checkPayloadFromForm("ssh", form)).toEqual({ check: { banner: "SSH-2.0-OpenSSH" }, secrets: null })
	expect(checkPayloadFromForm("smtp", form)).toEqual({ check: { startTLS: true, ignoreTLS: true }, secrets: null })
	// STARTTLS is not a TLS mode of tcp
	expect(checkPayloadFromForm("tcp", form)).toEqual({ check: { banner: "SSH-2.0-OpenSSH" }, secrets: null })
	expect(checkPayloadFromForm("redis", { ...form, tlsMode: "tls" })).toEqual({
		check: { tls: true, ignoreTLS: true },
		secrets: { username: "app", password: "secret" },
	})
	expect(checkPayloadFromForm("grpc", form)).toEqual({ check: { service: "health" }, secrets: null })
	expect(checkPayloadFromForm("mysql", form)).toEqual({ check: null, secrets: null })
	expect(checkPayloadFromForm("dns", emptyForm)).toEqual({ check: null, secrets: null })
	expect(hasCustomCheckOptions("icmp", form)).toBe(false)
	expect(hasCustomCheckOptions("postgres", { ...emptyForm, password: "x" })).toBe(true)
})

test("check form round trips a monitor", () => {
	const monitor = {
		check: { tls: true, ignoreTLS: true, recordType: "TXT" as const, expected: "v=spf1", matchMode: "equals" as const },
		httpSecrets: { username: "u", password: "p" },
	}
	const form = checkFormFromMonitor(monitor)
	expect(form.tlsMode).toBe("tls")
	expect(form.matchMode).toBe("equals")
	expect(checkPayloadFromForm("postgres", form)).toEqual({
		check: { tls: true, ignoreTLS: true },
		secrets: { username: "u", password: "p" },
	})
	expect(checkFormFromMonitor({ check: { startTLS: true } }).tlsMode).toBe("starttls")
})

test("cert reporting follows TLS use", () => {
	expect(monitorReportsCert("http", "https://example.com", emptyForm)).toBe(true)
	expect(monitorReportsCert("http", "http://example.com", emptyForm)).toBe(false)
	expect(monitorReportsCert("imap", "mail", { ...emptyForm, tlsMode: "starttls" })).toBe(true)
	expect(monitorReportsCert("grpc", "h", emptyForm)).toBe(false)
	expect(monitorReportsCert("ssh", "h", { ...emptyForm, tlsMode: "tls" })).toBe(false)
})
