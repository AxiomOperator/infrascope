import type { MonitorCheckOptions, MonitorProtocol } from "@/types"

/** All monitor protocols, in display order. */
export const monitorProtocols: MonitorProtocol[] = [
	"icmp",
	"tcp",
	"dns",
	"ssh",
	"http",
	"grpc",
	"postgres",
	"mysql",
	"redis",
	"smtp",
	"imap",
	"minecraft",
	"a2s",
	"docker",
	"push",
]

/** Display names of monitor protocols. */
export const monitorProtocolLabels: Record<MonitorProtocol, string> = {
	icmp: "ICMP",
	tcp: "TCP",
	http: "HTTP",
	dns: "DNS",
	push: "Push",
	ssh: "SSH",
	postgres: "PostgreSQL",
	mysql: "MySQL",
	redis: "Redis",
	smtp: "SMTP",
	imap: "IMAP",
	grpc: "gRPC",
	minecraft: "Minecraft",
	a2s: "A2S",
	docker: "Docker",
}

/** Display name of a protocol; unknown values are shown upper case. */
export function getMonitorProtocolLabel(protocol: string) {
	return monitorProtocolLabels[protocol as MonitorProtocol] ?? protocol.toUpperCase()
}

const portProtocols = new Set<MonitorProtocol>([
	"tcp",
	"ssh",
	"postgres",
	"mysql",
	"redis",
	"smtp",
	"imap",
	"grpc",
	"minecraft",
	"a2s",
])

/** Whether monitors of the protocol connect to a host and port. */
export function usesMonitorPort(protocol: string) {
	return portProtocols.has(protocol as MonitorProtocol)
}

/** Protocols added in agent 0.21.0, which older agents can't run. */
const checkProtocols = new Set<MonitorProtocol>([
	"ssh",
	"postgres",
	"mysql",
	"redis",
	"smtp",
	"imap",
	"grpc",
	"minecraft",
	"a2s",
	"docker",
])

/** Whether the protocol needs agent version 0.21.0 or newer. */
export function isCheckProtocol(protocol: string) {
	return checkProtocols.has(protocol as MonitorProtocol)
}

/** Protocols that only run on an agent. */
export function isAgentOnlyProtocol(protocol: string) {
	return protocol === "docker"
}

/**
 * Well-known port of a protocol, which depends on the TLS options for smtp and imap.
 * 0 when the protocol has no default (tcp and grpc need a port).
 */
export function defaultMonitorPort(
	protocol: string,
	check?: Pick<MonitorCheckOptions, "tls" | "startTLS"> | null
): number {
	switch (protocol) {
		case "ssh":
			return 22
		case "postgres":
			return 5432
		case "mysql":
			return 3306
		case "redis":
			return 6379
		case "smtp":
			return check?.tls ? 465 : check?.startTLS ? 587 : 25
		case "imap":
			return check?.tls ? 993 : 143
		case "minecraft":
			return 25565
		case "a2s":
			return 27015
		default:
			return 0
	}
}

/** Protocols whose checks can use TLS. */
const tlsProtocols = new Set<MonitorProtocol>(["tcp", "postgres", "redis", "smtp", "imap", "grpc"])

/** Whether the protocol can connect with TLS (and so report a certificate). */
export function supportsTls(protocol: string) {
	return tlsProtocols.has(protocol as MonitorProtocol)
}

/** Protocols whose checks can upgrade with STARTTLS. */
export function supportsStartTls(protocol: string) {
	return protocol === "smtp" || protocol === "imap"
}

/** Whether a check with these options uses TLS and reports a certificate. */
export function checkUsesTls(protocol: string, check?: MonitorCheckOptions | null) {
	if (!supportsTls(protocol) || !check) return false
	return !!check.tls || (supportsStartTls(protocol) && !!check.startTLS)
}

/** Docker container names and IDs. */
export const containerRefPattern = /^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,254}$/
