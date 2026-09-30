import { t } from "@lingui/core/macro"
import { Trans } from "@lingui/react/macro"
import { ChevronDownIcon, EyeIcon, EyeOffIcon, PlusIcon, Trash2Icon } from "lucide-react"
import { useId, useState } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { cn } from "@/lib/utils"
import { type HttpFormState, type HttpMethod, httpMethods, newHeaderRow } from "./monitor-form-utils"

const redirectCounts = Array.from({ length: 20 }, (_, i) => i + 1)

/** Whether the form differs from the default HTTP options. */
export function hasCustomHttpOptions(form: HttpFormState) {
	return (
		form.method !== "GET" ||
		!!form.acceptedCodes.trim() ||
		form.maxRedirects !== 0 ||
		form.ignoreTLS ||
		!!form.keyword ||
		!!form.jsonPath.trim() ||
		form.headers.some((header) => header.name || header.value) ||
		!!form.body ||
		!!form.basicUser ||
		!!form.basicPass
	)
}

/** Collapsible HTTP request and response options of an http monitor. */
export function MonitorHttpOptions({
	value,
	onChange,
	secretsHidden,
	disabled,
	locked,
}: {
	value: HttpFormState
	onChange: (value: HttpFormState) => void
	/** Secret options exist but aren't visible to this user; they are kept unchanged. */
	secretsHidden?: boolean
	disabled?: boolean
	/** Reports whether an option is set by Docker labels and read-only. */
	locked?: (option: "keyword" | "acceptedCodes") => boolean
}) {
	const [open, setOpen] = useState(() => hasCustomHttpOptions(value))
	const [showSecrets, setShowSecrets] = useState(false)
	const id = useId()
	const set = <K extends keyof HttpFormState>(key: K, fieldValue: HttpFormState[K]) =>
		onChange({ ...value, [key]: fieldValue })
	const hasBody = value.method !== "GET" && value.method !== "HEAD"

	return (
		<div className="rounded-lg border">
			<button
				type="button"
				className="flex w-full items-center justify-between gap-2 px-3 py-2.5 text-sm font-medium hover:bg-accent/50 rounded-lg"
				aria-expanded={open}
				onClick={() => setOpen(!open)}
			>
				<Trans>HTTP options</Trans>
				<ChevronDownIcon className={cn("size-4 opacity-60 transition-transform", open && "rotate-180")} />
			</button>
			{open && (
				<div className="grid gap-4 border-t p-3">
					<div className="grid grid-cols-2 gap-3">
						<div className="grid gap-2">
							<Label htmlFor={`${id}-method`}>
								<Trans>Method</Trans>
							</Label>
							<Select
								value={value.method}
								onValueChange={(method) => set("method", method as HttpMethod)}
								disabled={disabled}
							>
								<SelectTrigger id={`${id}-method`}>
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									{httpMethods.map((method) => (
										<SelectItem key={method} value={method}>
											{method}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>
						<div className="grid gap-2">
							<Label htmlFor={`${id}-redirects`}>
								<Trans>Max redirects</Trans>
							</Label>
							<Select
								value={String(value.maxRedirects)}
								onValueChange={(redirects) => set("maxRedirects", Number(redirects))}
								disabled={disabled}
							>
								<SelectTrigger id={`${id}-redirects`}>
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									<SelectItem value="0">{t`Default (10)`}</SelectItem>
									<SelectItem value="-1">{t`Don't follow`}</SelectItem>
									{redirectCounts.map((count) => (
										<SelectItem key={count} value={String(count)}>
											{count}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>
					</div>
					<div className="grid gap-2">
						<Label htmlFor={`${id}-codes`}>
							<Trans>Accepted status codes</Trans>
						</Label>
						<Input
							id={`${id}-codes`}
							value={value.acceptedCodes}
							onChange={(e) => set("acceptedCodes", e.target.value)}
							readOnly={locked?.("acceptedCodes")}
							placeholder="200-399"
							disabled={disabled}
						/>
						<p className="text-xs text-muted-foreground">
							<Trans>Comma separated codes or ranges, e.g. 200-299, 301. Defaults to 200-399.</Trans>
						</p>
					</div>
					<SwitchField
						id={`${id}-tls`}
						checked={value.ignoreTLS}
						onCheckedChange={(checked) => set("ignoreTLS", checked)}
						disabled={disabled}
						label={<Trans>Ignore TLS errors</Trans>}
					/>
					<div className="grid gap-2">
						<Label htmlFor={`${id}-keyword`}>
							<Trans>Keyword</Trans>
						</Label>
						<Input
							id={`${id}-keyword`}
							value={value.keyword}
							onChange={(e) => set("keyword", e.target.value)}
							readOnly={locked?.("keyword")}
							placeholder={t`Text the response body must contain`}
							disabled={disabled}
						/>
						{value.keyword && (
							<SwitchField
								id={`${id}-invert`}
								checked={value.keywordInvert}
								onCheckedChange={(checked) => set("keywordInvert", checked)}
								disabled={disabled}
								label={<Trans>Invert keyword (fail when found)</Trans>}
							/>
						)}
					</div>
					<div className="grid grid-cols-2 gap-3">
						<div className="grid gap-2">
							<Label htmlFor={`${id}-json-path`}>
								<Trans>JSON path</Trans>
							</Label>
							<Input
								id={`${id}-json-path`}
								value={value.jsonPath}
								onChange={(e) => set("jsonPath", e.target.value)}
								placeholder="data.status"
								className="font-mono"
								disabled={disabled}
							/>
						</div>
						<div className="grid gap-2">
							<Label htmlFor={`${id}-json-expected`}>
								<Trans>Expected value</Trans>
							</Label>
							<Input
								id={`${id}-json-expected`}
								value={value.jsonExpected}
								onChange={(e) => set("jsonExpected", e.target.value)}
								placeholder="ok"
								disabled={disabled || !value.jsonPath.trim()}
							/>
						</div>
					</div>
					{secretsHidden ? (
						<p className="text-xs text-muted-foreground">
							<Trans>Headers, body and credentials aren't visible to you and are kept unchanged.</Trans>
						</p>
					) : (
						<>
							<div className="grid gap-2">
								<div className="flex items-center justify-between gap-2">
									<Label>
										<Trans>Headers</Trans>
									</Label>
									<div className="flex items-center gap-1">
										{value.headers.length > 0 && (
											<Button
												type="button"
												variant="ghost"
												size="sm"
												className="h-7 px-2 text-xs"
												onClick={() => setShowSecrets(!showSecrets)}
											>
												{showSecrets ? <EyeOffIcon className="size-3.5 me-1" /> : <EyeIcon className="size-3.5 me-1" />}
												{showSecrets ? <Trans>Hide values</Trans> : <Trans>Show values</Trans>}
											</Button>
										)}
										<Button
											type="button"
											variant="ghost"
											size="sm"
											className="h-7 px-2 text-xs"
											disabled={disabled}
											onClick={() => set("headers", [...value.headers, newHeaderRow()])}
										>
											<PlusIcon className="size-3.5 me-1" />
											<Trans>Add header</Trans>
										</Button>
									</div>
								</div>
								{value.headers.map((header) => (
									<div key={header.id} className="flex gap-2">
										<Input
											value={header.name}
											aria-label={t`Header name`}
											placeholder={t`Name`}
											className="w-2/5 font-mono text-xs"
											disabled={disabled}
											onChange={(e) =>
												set(
													"headers",
													value.headers.map((h) => (h.id === header.id ? { ...h, name: e.target.value } : h))
												)
											}
										/>
										<Input
											value={header.value}
											type={showSecrets ? "text" : "password"}
											autoComplete="off"
											aria-label={t`Header value`}
											placeholder={t`Value`}
											className="grow font-mono text-xs"
											disabled={disabled}
											onChange={(e) =>
												set(
													"headers",
													value.headers.map((h) => (h.id === header.id ? { ...h, value: e.target.value } : h))
												)
											}
										/>
										<Button
											type="button"
											variant="ghost"
											size="icon"
											className="shrink-0"
											aria-label={t`Remove header`}
											disabled={disabled}
											onClick={() =>
												set(
													"headers",
													value.headers.filter((h) => h.id !== header.id)
												)
											}
										>
											<Trash2Icon className="size-4" />
										</Button>
									</div>
								))}
							</div>
							{hasBody && (
								<div className="grid gap-2">
									<Label htmlFor={`${id}-body`}>
										<Trans>Body</Trans>
									</Label>
									<Textarea
										id={`${id}-body`}
										value={value.body}
										onChange={(e) => set("body", e.target.value)}
										className="font-mono text-xs min-h-20"
										placeholder={'{"key": "value"}'}
										disabled={disabled}
									/>
								</div>
							)}
							<div className="grid grid-cols-2 gap-3">
								<div className="grid gap-2">
									<Label htmlFor={`${id}-basic-user`}>
										<Trans>Basic auth user</Trans>
									</Label>
									<Input
										id={`${id}-basic-user`}
										value={value.basicUser}
										onChange={(e) => set("basicUser", e.target.value)}
										autoComplete="off"
										disabled={disabled}
									/>
								</div>
								<div className="grid gap-2">
									<Label htmlFor={`${id}-basic-pass`}>
										<Trans>Basic auth password</Trans>
									</Label>
									<Input
										id={`${id}-basic-pass`}
										type="password"
										value={value.basicPass}
										onChange={(e) => set("basicPass", e.target.value)}
										autoComplete="new-password"
										disabled={disabled}
									/>
								</div>
							</div>
						</>
					)}
				</div>
			)}
		</div>
	)
}

export function SwitchField({
	id,
	checked,
	onCheckedChange,
	label,
	description,
	disabled,
}: {
	id: string
	checked: boolean
	onCheckedChange: (checked: boolean) => void
	label: React.ReactNode
	description?: React.ReactNode
	disabled?: boolean
}) {
	return (
		<div className="flex items-start justify-between gap-3">
			<div className="grid gap-1">
				<Label htmlFor={id} className="leading-snug">
					{label}
				</Label>
				{description && <p className="text-xs text-muted-foreground">{description}</p>}
			</div>
			<Switch id={id} checked={checked} onCheckedChange={onCheckedChange} disabled={disabled} />
		</div>
	)
}
