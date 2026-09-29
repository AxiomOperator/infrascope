import { t } from "@lingui/core/macro"
import { Trans } from "@lingui/react/macro"
import { ChevronDownIcon } from "lucide-react"
import { useId, useState } from "react"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { supportsStartTls, supportsTls } from "@/lib/monitor-protocols"
import { cn } from "@/lib/utils"
import type { MonitorDNSRecordType } from "@/types"
import {
	type CheckFormState,
	dnsRecordTypes,
	hasCustomCheckOptions,
	type MonitorProtocol,
	type TlsMode,
	usesCheckCredentials,
} from "./monitor-form-utils"
import { SwitchField } from "./monitor-http-options"

/** Protocols with options in the check options section. */
export function hasCheckOptions(protocol: MonitorProtocol) {
	return protocol === "dns" || protocol === "ssh" || supportsTls(protocol)
}

/** Short description of what a protocol's check verifies, if it needs one. */
export function MonitorCheckDescription({ protocol }: { protocol: MonitorProtocol }) {
	let text: React.ReactNode = null
	switch (protocol) {
		case "ssh":
			text = <Trans>Checks that the server sends an SSH-2.0 identification. No login is attempted.</Trans>
			break
		case "postgres":
			text = <Trans>Checks that the server speaks PostgreSQL; with a password, also that it can log in.</Trans>
			break
		case "mysql":
			text = <Trans>Checks that the server sends a valid MySQL handshake. No login is attempted.</Trans>
			break
		case "redis":
			text = <Trans>Sends PING and expects PONG, authenticating first when a password is set.</Trans>
			break
		case "smtp":
			text = <Trans>Checks the SMTP greeting and EHLO reply.</Trans>
			break
		case "imap":
			text = <Trans>Checks the IMAP greeting.</Trans>
			break
		case "grpc":
			text = <Trans>Calls the standard gRPC health check and expects SERVING.</Trans>
			break
		case "minecraft":
			text = <Trans>Sends a Java Edition server list ping.</Trans>
			break
		case "a2s":
			text = <Trans>Sends a Source engine A2S_INFO query over UDP.</Trans>
			break
		case "docker":
			text = (
				<Trans>
					Checks that the container is running and, if it has a health check, healthy. Runs on the agent's Docker or
					Podman host.
				</Trans>
			)
			break
	}
	return text ? <p className="-mt-2 text-xs text-muted-foreground">{text}</p> : null
}

/** Collapsible protocol-specific check options. */
export function MonitorCheckOptions({
	protocol,
	value,
	onChange,
	secretsHidden,
	disabled,
}: {
	protocol: MonitorProtocol
	value: CheckFormState
	onChange: (value: CheckFormState) => void
	/** Credentials exist but aren't visible to this user; they are kept unchanged. */
	secretsHidden?: boolean
	disabled?: boolean
}) {
	const [open, setOpen] = useState(() => hasCustomCheckOptions(protocol, value))
	const id = useId()
	const set = <K extends keyof CheckFormState>(key: K, fieldValue: CheckFormState[K]) =>
		onChange({ ...value, [key]: fieldValue })
	if (!hasCheckOptions(protocol)) {
		return null
	}
	const startTls = supportsStartTls(protocol)
	const tlsMode: TlsMode = !startTls && value.tlsMode === "starttls" ? "none" : value.tlsMode
	const usesTls = supportsTls(protocol) && tlsMode !== "none"

	return (
		<div className="rounded-lg border">
			<button
				type="button"
				className="flex w-full items-center justify-between gap-2 px-3 py-2.5 text-sm font-medium hover:bg-accent/50 rounded-lg"
				aria-expanded={open}
				onClick={() => setOpen(!open)}
			>
				<Trans>Check options</Trans>
				<ChevronDownIcon className={cn("size-4 opacity-60 transition-transform", open && "rotate-180")} />
			</button>
			{open && (
				<div className="grid gap-4 border-t p-3">
					{protocol === "dns" && (
						<>
							<div className="grid grid-cols-2 gap-3">
								<div className="grid gap-2">
									<Label htmlFor={`${id}-record-type`}>
										<Trans>Record type</Trans>
									</Label>
									<Select
										value={value.recordType || "default"}
										onValueChange={(type) =>
											set("recordType", type === "default" ? "" : (type as MonitorDNSRecordType))
										}
										disabled={disabled}
									>
										<SelectTrigger id={`${id}-record-type`}>
											<SelectValue />
										</SelectTrigger>
										<SelectContent>
											<SelectItem value="default">{t`A and AAAA`}</SelectItem>
											{dnsRecordTypes.map((type) => (
												<SelectItem key={type} value={type}>
													{type}
												</SelectItem>
											))}
										</SelectContent>
									</Select>
								</div>
								<div className="grid gap-2">
									<Label htmlFor={`${id}-match-mode`}>
										<Trans>Match</Trans>
									</Label>
									<Select
										value={value.matchMode}
										onValueChange={(mode) => set("matchMode", mode as CheckFormState["matchMode"])}
										disabled={disabled || !value.expected.trim()}
									>
										<SelectTrigger id={`${id}-match-mode`}>
											<SelectValue />
										</SelectTrigger>
										<SelectContent>
											<SelectItem value="contains">{t`Contains`}</SelectItem>
											<SelectItem value="equals">{t`Equals`}</SelectItem>
										</SelectContent>
									</Select>
								</div>
							</div>
							<div className="grid gap-2">
								<Label htmlFor={`${id}-expected`}>
									<Trans>Expected value</Trans>
								</Label>
								<Input
									id={`${id}-expected`}
									value={value.expected}
									onChange={(e) => set("expected", e.target.value)}
									placeholder={t`Optional`}
									maxLength={500}
									disabled={disabled}
								/>
								<p className="text-xs text-muted-foreground">
									<Trans>One of the records must match this value.</Trans>
								</p>
							</div>
						</>
					)}
					{(protocol === "tcp" || protocol === "ssh") && (
						<div className="grid gap-2">
							<Label htmlFor={`${id}-banner`}>
								<Trans>Expected banner</Trans>
							</Label>
							<Input
								id={`${id}-banner`}
								value={value.banner}
								onChange={(e) => set("banner", e.target.value)}
								placeholder={protocol === "ssh" ? "SSH-2.0" : t`Optional`}
								maxLength={256}
								disabled={disabled}
							/>
							<p className="text-xs text-muted-foreground">
								<Trans>Text the server must send after connecting.</Trans>
							</p>
						</div>
					)}
					{startTls ? (
						<div className="grid gap-2">
							<Label htmlFor={`${id}-tls-mode`}>
								<Trans>Encryption</Trans>
							</Label>
							<Select value={tlsMode} onValueChange={(mode) => set("tlsMode", mode as TlsMode)} disabled={disabled}>
								<SelectTrigger id={`${id}-tls-mode`}>
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									<SelectItem value="none">{t`None`}</SelectItem>
									<SelectItem value="starttls">STARTTLS</SelectItem>
									<SelectItem value="tls">TLS</SelectItem>
								</SelectContent>
							</Select>
						</div>
					) : (
						supportsTls(protocol) && (
							<SwitchField
								id={`${id}-tls`}
								checked={tlsMode === "tls"}
								onCheckedChange={(checked) => set("tlsMode", checked ? "tls" : "none")}
								disabled={disabled}
								label={<Trans>Use TLS</Trans>}
							/>
						)
					)}
					{usesTls && (
						<SwitchField
							id={`${id}-ignore-tls`}
							checked={value.ignoreTLS}
							onCheckedChange={(checked) => set("ignoreTLS", checked)}
							disabled={disabled}
							label={<Trans>Ignore TLS errors</Trans>}
						/>
					)}
					{protocol === "grpc" && (
						<div className="grid gap-2">
							<Label htmlFor={`${id}-service`}>
								<Trans>Service</Trans>
							</Label>
							<Input
								id={`${id}-service`}
								value={value.service}
								onChange={(e) => set("service", e.target.value)}
								placeholder={t`Empty checks the whole server`}
								maxLength={500}
								disabled={disabled}
							/>
						</div>
					)}
					{usesCheckCredentials(protocol) &&
						(secretsHidden ? (
							<p className="text-xs text-muted-foreground">
								<Trans>Credentials aren't visible to you and are kept unchanged.</Trans>
							</p>
						) : (
							<div className="grid grid-cols-2 gap-3">
								<div className="grid gap-2">
									<Label htmlFor={`${id}-username`}>
										<Trans>Username</Trans>
									</Label>
									<Input
										id={`${id}-username`}
										value={value.username}
										onChange={(e) => set("username", e.target.value)}
										placeholder={protocol === "postgres" ? "postgres" : t`Optional`}
										autoComplete="off"
										maxLength={500}
										disabled={disabled}
									/>
								</div>
								<div className="grid gap-2">
									<Label htmlFor={`${id}-password`}>
										<Trans>Password</Trans>
									</Label>
									<Input
										id={`${id}-password`}
										type="password"
										value={value.password}
										onChange={(e) => set("password", e.target.value)}
										placeholder={t`Optional`}
										autoComplete="new-password"
										maxLength={500}
										disabled={disabled}
									/>
								</div>
							</div>
						))}
				</div>
			)}
		</div>
	)
}
