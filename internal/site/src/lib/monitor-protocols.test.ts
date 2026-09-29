import { expect, test } from "bun:test"
import {
	checkUsesTls,
	defaultMonitorPort,
	getMonitorProtocolLabel,
	isCheckProtocol,
	monitorProtocols,
	usesMonitorPort,
} from "./monitor-protocols"

test("protocol metadata", () => {
	expect(getMonitorProtocolLabel("grpc")).toBe("gRPC")
	expect(getMonitorProtocolLabel("ftp")).toBe("FTP")
	expect(monitorProtocols).toHaveLength(15)
	expect(usesMonitorPort("a2s")).toBe(true)
	expect(usesMonitorPort("docker")).toBe(false)
	expect(isCheckProtocol("docker")).toBe(true)
	expect(isCheckProtocol("http")).toBe(false)
})

test("default ports", () => {
	expect(defaultMonitorPort("smtp", { tls: true })).toBe(465)
	expect(defaultMonitorPort("smtp", { startTLS: true })).toBe(587)
	expect(defaultMonitorPort("imap")).toBe(143)
	expect(defaultMonitorPort("minecraft")).toBe(25565)
	expect(defaultMonitorPort("tcp")).toBe(0)
})

test("TLS use", () => {
	expect(checkUsesTls("tcp", { tls: true })).toBe(true)
	expect(checkUsesTls("tcp", { startTLS: true })).toBe(false)
	expect(checkUsesTls("smtp", { startTLS: true })).toBe(true)
	expect(checkUsesTls("ssh", { tls: true })).toBe(false)
	expect(checkUsesTls("redis", null)).toBe(false)
})
