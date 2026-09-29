import { t } from "@lingui/core/macro"
import { Trans } from "@lingui/react/macro"
import {
	ArrowLeftIcon,
	BookOpenIcon,
	ChevronDownIcon,
	ClipboardPasteIcon,
	CodeIcon,
	EyeIcon,
	EyeOffIcon,
	LinkIcon,
	ListIcon,
	LoaderCircleIcon,
	PencilIcon,
	SearchIcon,
	SendIcon,
	Trash2Icon,
} from "lucide-react"
import type { ClientResponseError } from "pocketbase"
import { useMemo, useState } from "react"
import * as v from "valibot"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { toast } from "@/components/ui/use-toast"
import { pb } from "@/lib/api"
import {
	detectService,
	type Field,
	type FieldValue,
	type FieldValues,
	getService,
	maskUnknownUrl,
	maskUrl,
	type ServiceValues,
	type ShoutrrrService,
	services,
} from "@/lib/shoutrrr"
import { cn } from "@/lib/utils"

export const CUSTOM_ID = "custom"

/** valibot schema shared with the settings page */
export const WebhookUrlSchema = v.pipe(v.string(), v.url())

// ---------------------------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------------------------

export async function sendTestNotification(url: string): Promise<boolean> {
	const showError = (msg?: string) =>
		toast({ title: t`Error`, description: msg || t`Failed to send test notification`, variant: "destructive" })
	try {
		const res = await pb.send("/api/beszel/test-notification", { method: "POST", body: { url } })
		if ("err" in res && !res.err) {
			toast({ title: t`Test notification sent`, description: t`Check your notification service` })
			return true
		}
		showError(res.err)
	} catch (e: unknown) {
		showError((e as ClientResponseError).data?.message)
	}
	return false
}

/** name, masked summary and masked URL for a stored Shoutrrr URL */
export function describeUrl(url: string) {
	const detected = detectService(url)
	if (!detected) {
		return { service: undefined, name: t`Custom URL`, summary: "", masked: maskUnknownUrl(url) }
	}
	const { service, values } = detected
	return {
		service,
		name: service.name,
		summary: service.summary(values),
		masked: maskUrl(url, service.secrets(values)),
	}
}

function ServiceBadge({ service, className }: { service?: ShoutrrrService; className?: string }) {
	const letter = service ? service.name.charAt(0).toUpperCase() : undefined
	return (
		<span
			aria-hidden="true"
			className={cn(
				"flex size-9 shrink-0 items-center justify-center rounded-md border bg-muted text-sm font-semibold text-muted-foreground",
				className
			)}
		>
			{letter ?? <LinkIcon className="size-4" />}
		</span>
	)
}

// ---------------------------------------------------------------------------------------------
// Saved entry card
// ---------------------------------------------------------------------------------------------

export function NotificationCard({ url, onEdit, onDelete }: { url: string; onEdit: () => void; onDelete: () => void }) {
	const [testing, setTesting] = useState(false)
	const info = useMemo(() => describeUrl(url), [url])

	const test = async () => {
		setTesting(true)
		await sendTestNotification(url)
		setTesting(false)
	}

	return (
		<Card className="bg-table-header p-2.5 md:p-3">
			<div className="flex flex-wrap items-center gap-3 sm:flex-nowrap">
				<ServiceBadge service={info.service} className="light:bg-card" />
				<div className="min-w-0 flex-1">
					<div className="flex min-w-0 items-baseline gap-1.5 text-sm">
						<span className="font-medium shrink-0">{info.name}</span>
						{info.summary && <span className="truncate text-muted-foreground">· {info.summary}</span>}
					</div>
					<p className="truncate font-mono text-xs text-muted-foreground" title={info.masked}>
						{info.masked}
					</p>
				</div>
				<div className="flex shrink-0 items-center gap-1 max-sm:w-full max-sm:justify-end">
					<Button type="button" variant="outline" size="sm" className="h-9" disabled={testing} onClick={test}>
						{testing ? <LoaderCircleIcon className="size-4 animate-spin" /> : <SendIcon className="size-4" />}
						<span className="ms-1">
							<Trans>Test</Trans>
						</span>
					</Button>
					<Button
						type="button"
						variant="outline"
						size="icon"
						className="size-9"
						aria-label={t`Edit ${info.name} notification`}
						title={t`Edit`}
						onClick={onEdit}
					>
						<PencilIcon className="size-4" />
					</Button>
					<Button
						type="button"
						variant="outline"
						size="icon"
						className="size-9"
						aria-label={t`Delete ${info.name} notification`}
						title={t`Delete`}
						onClick={onDelete}
					>
						<Trash2Icon className="size-4" />
					</Button>
				</div>
			</div>
		</Card>
	)
}

// ---------------------------------------------------------------------------------------------
// Dialog
// ---------------------------------------------------------------------------------------------

interface DialogState {
	serviceId: string | null
	values: ServiceValues
	rawMode: boolean
	rawText: string
}

function initialState(url: string | null): DialogState {
	if (url === null) return { serviceId: null, values: { fields: {}, extra: [] }, rawMode: false, rawText: "" }
	const detected = detectService(url)
	if (!detected) return { serviceId: CUSTOM_ID, values: { fields: {}, extra: [] }, rawMode: true, rawText: url }
	return { serviceId: detected.service.id, values: detected.values, rawMode: false, rawText: url }
}

/**
 * Add / edit dialog. `url` is the entry being edited (null when adding). `onSubmit` receives the final
 * Shoutrrr URL; persisting happens with the page's "Save Settings" button.
 */
export function NotificationDialog({
	url,
	onSubmit,
	onCancel,
}: {
	url: string | null
	onSubmit: (url: string) => void
	onCancel: () => void
}) {
	const [state, setState] = useState<DialogState>(() => initialState(url))
	const [search, setSearch] = useState("")
	const [reveal, setReveal] = useState(false)
	const [showAdvanced, setShowAdvanced] = useState(false)
	const [showErrors, setShowErrors] = useState(false)
	const [testing, setTesting] = useState(false)
	const [pasteNote, setPasteNote] = useState<string | null>(null)
	const [pasteText, setPasteText] = useState("")

	const isEdit = url !== null
	const service = state.serviceId && state.serviceId !== CUSTOM_ID ? getService(state.serviceId) : undefined
	const isCustom = state.serviceId === CUSTOM_ID

	const fieldValues: FieldValues = service ? { ...service.defaults().fields, ...state.values.fields } : {}
	const errors = service && !state.rawMode ? service.validate(state.values) : {}
	const builtUrl = state.rawMode || !service ? state.rawText.trim() : service.build(state.values)
	const rawDetected = state.rawMode ? detectService(state.rawText) : null
	const urlValid = v.is(WebhookUrlSchema, builtUrl)
	const formValid = Object.keys(errors).length === 0 && urlValid

	const pickService = (id: string) => {
		const s = getService(id)
		setShowErrors(false)
		setShowAdvanced(false)
		setPasteNote(null)
		setPasteText("")
		if (s) setState({ serviceId: id, values: s.defaults(), rawMode: false, rawText: "" })
		else setState({ serviceId: CUSTOM_ID, values: { fields: {}, extra: [] }, rawMode: true, rawText: "" })
	}

	const setField = (key: string, value: FieldValue) =>
		setState((s) => ({ ...s, values: { ...s.values, fields: { ...s.values.fields, [key]: value } } }))

	const setRawText = (text: string) => {
		setState((s) => {
			const detected = detectService(text)
			// keep the fields in sync so switching back to the form shows what was typed
			if (detected && !isCustom) return { ...s, rawText: text, serviceId: detected.service.id, values: detected.values }
			return { ...s, rawText: text }
		})
	}

	const toggleRaw = () => {
		if (state.rawMode) {
			const detected = detectService(state.rawText)
			if (!detected) return
			setState({ serviceId: detected.service.id, values: detected.values, rawMode: false, rawText: "" })
		} else if (service) {
			setState((s) => ({ ...s, rawMode: true, rawText: service.build(s.values) }))
		}
	}

	const applyPaste = (input: string) => {
		if (!service?.paste || !input.trim()) return
		const extracted = service.paste.extract(input)
		if (!extracted) {
			setPasteText(input)
			setPasteNote(t`That doesn't look like a ${service.name} webhook URL.`)
			return
		}
		setPasteText("")
		setState((s) => ({ ...s, values: { ...s.values, fields: { ...s.values.fields, ...extracted } } }))
		setPasteNote(t`Fields filled from the webhook URL.`)
	}

	const test = async () => {
		setShowErrors(true)
		if (!formValid) return
		setTesting(true)
		await sendTestNotification(builtUrl)
		setTesting(false)
	}

	const submit = (e: React.FormEvent) => {
		e.preventDefault()
		setShowErrors(true)
		if (!formValid) return
		onSubmit(builtUrl)
	}

	// ---------------- step 1: service picker ----------------
	if (!state.serviceId) {
		const q = search.trim().toLowerCase()
		const matches = services.filter((s) => !q || s.name.toLowerCase().includes(q) || s.id.includes(q))
		const showCustom = !q || t`Custom URL`.toLowerCase().includes(q) || "shoutrrr".includes(q)
		return (
			<DialogContent className="max-h-[calc(100dvh-2rem)] w-[calc(100%-2rem)] overflow-y-auto rounded-lg sm:max-w-2xl">
				<DialogHeader>
					<DialogTitle>
						<Trans>Add notification</Trans>
					</DialogTitle>
					<DialogDescription>
						<Trans>Choose where alerts should be sent.</Trans>
					</DialogDescription>
				</DialogHeader>
				<div className="relative">
					<SearchIcon className="pointer-events-none absolute start-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
					<Input
						autoFocus
						className="ps-9"
						placeholder={t`Search services...`}
						aria-label={t`Search services`}
						value={search}
						onChange={(e) => setSearch(e.target.value)}
					/>
				</div>
				<ul className="grid grid-cols-2 gap-2 sm:grid-cols-3" aria-label={t`Notification services`}>
					{matches.map((s) => (
						<li key={s.id}>
							<button
								type="button"
								onClick={() => pickService(s.id)}
								className="flex w-full items-center gap-2.5 rounded-md border p-2 text-start text-sm transition-colors hover:bg-accent focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-ring"
							>
								<ServiceBadge service={s} className="size-8" />
								<span className="min-w-0 truncate font-medium">{s.name}</span>
							</button>
						</li>
					))}
					{showCustom && (
						<li>
							<button
								type="button"
								onClick={() => pickService(CUSTOM_ID)}
								className="flex w-full items-center gap-2.5 rounded-md border border-dashed p-2 text-start text-sm transition-colors hover:bg-accent focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-ring"
							>
								<ServiceBadge className="size-8" />
								<span className="min-w-0 truncate font-medium">
									<Trans>Custom URL</Trans>
								</span>
							</button>
						</li>
					)}
				</ul>
				{matches.length === 0 && !showCustom && (
					<p className="text-sm text-muted-foreground">
						<Trans>No matching services.</Trans>
					</p>
				)}
			</DialogContent>
		)
	}

	// ---------------- step 2: form ----------------
	const visibleFields = service ? service.fields.filter((f) => !f.visible || f.visible(fieldValues)) : []
	const basicFields = visibleFields.filter((f) => !f.advanced)
	const advancedFields = visibleFields.filter((f) => f.advanced)
	const displayUrl =
		reveal || !builtUrl
			? builtUrl
			: service
				? maskUrl(builtUrl, service.secrets(state.values))
				: maskUnknownUrl(builtUrl)
	const serviceName = service?.name ?? t`Custom URL`

	const renderField = (field: Field) => (
		<FieldInput
			key={field.key}
			field={field}
			value={fieldValues[field.key]}
			reveal={reveal}
			error={showErrors ? errors[field.key] : undefined}
			onChange={(value) => setField(field.key, value)}
		/>
	)

	return (
		<DialogContent className="max-h-[calc(100dvh-2rem)] w-[calc(100%-2rem)] overflow-y-auto rounded-lg sm:max-w-xl">
			<DialogHeader>
				<div className="flex items-center gap-2 pe-6">
					{!isEdit && (
						<Button
							type="button"
							variant="ghost"
							size="icon"
							className="-ms-2 size-8 shrink-0"
							aria-label={t`Back to services`}
							onClick={() => setState(initialState(null))}
						>
							<ArrowLeftIcon className="size-4" />
						</Button>
					)}
					<ServiceBadge service={service} className="size-8" />
					<DialogTitle className="truncate">{serviceName}</DialogTitle>
				</div>
				<DialogDescription className="flex flex-wrap items-center gap-x-3 gap-y-1">
					{service ? (
						<a href={service.docsUrl} target="_blank" rel="noopener" className="link inline-flex items-center gap-1">
							<BookOpenIcon className="size-3.5" />
							<Trans>Setup guide</Trans>
						</a>
					) : (
						<span>
							<Trans>
								Enter any{" "}
								<a href="https://beszel.dev/guide/notifications" target="_blank" rel="noopener" className="link">
									Shoutrrr
								</a>{" "}
								URL.
							</Trans>
						</span>
					)}
				</DialogDescription>
			</DialogHeader>
			<form onSubmit={submit} className="grid min-w-0 gap-4" noValidate>
				{state.rawMode ? (
					<div className="grid gap-2">
						<Label htmlFor="nf-raw">
							<Trans>Shoutrrr URL</Trans>
						</Label>
						<Textarea
							id="nf-raw"
							rows={4}
							spellCheck={false}
							autoComplete="off"
							className="font-mono text-xs break-all"
							placeholder="generic://webhook.site/xxxxxx"
							value={state.rawText}
							aria-invalid={showErrors && !urlValid}
							aria-describedby="nf-raw-help"
							onChange={(e) => setRawText(e.target.value)}
						/>
						<p
							id="nf-raw-help"
							className={cn("text-xs", showErrors && !urlValid ? "text-destructive" : "text-muted-foreground")}
						>
							{showErrors && !urlValid ? (
								<Trans>Enter a valid URL, e.g. ntfy://ntfy.sh/topic</Trans>
							) : rawDetected ? (
								<Trans>Recognized as {rawDetected.service.name}.</Trans>
							) : state.rawText.trim() ? (
								<Trans>This URL can't be edited with the form. It will be saved exactly as entered.</Trans>
							) : null}
						</p>
					</div>
				) : (
					<>
						{service?.paste && (
							<div className="grid gap-2 rounded-md border border-dashed p-3">
								<Label htmlFor="nf-paste" className="flex items-center gap-1.5">
									<ClipboardPasteIcon className="size-4" />
									{service.paste.label()}
								</Label>
								<Input
									id="nf-paste"
									autoComplete="off"
									spellCheck={false}
									placeholder={service.paste.placeholder}
									aria-describedby={pasteNote ? "nf-paste-note" : undefined}
									value={pasteText}
									onChange={(e) => setPasteText(e.target.value)}
									onPaste={(e) => {
										const text = e.clipboardData.getData("text")
										if (text) {
											e.preventDefault()
											applyPaste(text)
										}
									}}
									onKeyDown={(e) => {
										if (e.key === "Enter") {
											e.preventDefault()
											applyPaste(pasteText)
										}
									}}
									onBlur={() => applyPaste(pasteText)}
								/>
								{pasteNote && (
									<p id="nf-paste-note" className="text-xs text-muted-foreground" aria-live="polite">
										{pasteNote}
									</p>
								)}
							</div>
						)}
						{basicFields.map(renderField)}
						{advancedFields.length > 0 && (
							<div className="grid gap-4">
								<button
									type="button"
									className="flex items-center gap-1 text-sm font-medium text-muted-foreground hover:text-foreground w-fit"
									aria-expanded={showAdvanced}
									onClick={() => setShowAdvanced((s) => !s)}
								>
									<ChevronDownIcon className={cn("size-4 transition-transform", showAdvanced && "rotate-180")} />
									<Trans>More options</Trans>
								</button>
								{(showAdvanced || (showErrors && advancedFields.some((f) => errors[f.key]))) &&
									advancedFields.map(renderField)}
							</div>
						)}
						{state.values.extra.length > 0 && (
							<p className="text-xs text-muted-foreground">
								<Trans>Other parameters kept from the URL: {state.values.extra.map(([k]) => k).join(", ")}</Trans>
							</p>
						)}
					</>
				)}

				{!state.rawMode && (
					<div className="grid gap-1.5">
						<span className="text-sm font-medium" id="nf-preview-label">
							<Trans>URL preview</Trans>
						</span>
						<code className="block min-h-9 rounded-md border bg-muted/50 px-3 py-2 font-mono text-xs break-all">
							{displayUrl}
						</code>
						{showErrors && !urlValid && Object.keys(errors).length === 0 && (
							<p className="text-xs text-destructive">
								<Trans>The generated URL is not valid.</Trans>
							</p>
						)}
					</div>
				)}
				<div className="flex flex-wrap gap-2">
					{!state.rawMode && (
						<Button
							type="button"
							variant="outline"
							size="sm"
							aria-pressed={reveal}
							onClick={() => setReveal((r) => !r)}
						>
							{reveal ? <EyeOffIcon className="size-4" /> : <EyeIcon className="size-4" />}
							<span className="ms-1">{reveal ? <Trans>Hide secrets</Trans> : <Trans>Show secrets</Trans>}</span>
						</Button>
					)}
					{(service || rawDetected) && (
						<Button
							type="button"
							variant="outline"
							size="sm"
							onClick={toggleRaw}
							disabled={state.rawMode && !rawDetected}
							title={state.rawMode && !rawDetected ? t`The URL can't be represented by the form` : undefined}
						>
							{state.rawMode ? <ListIcon className="size-4" /> : <CodeIcon className="size-4" />}
							<span className="ms-1">{state.rawMode ? <Trans>Edit as form</Trans> : <Trans>Edit as URL</Trans>}</span>
						</Button>
					)}
				</div>
				<DialogFooter className="gap-2 sm:gap-2">
					<Button type="button" variant="outline" disabled={testing || !builtUrl} onClick={test} className="sm:me-auto">
						{testing ? <LoaderCircleIcon className="size-4 animate-spin" /> : <SendIcon className="size-4" />}
						<span className="ms-1">
							<Trans>Send test</Trans>
						</span>
					</Button>
					<Button type="button" variant="ghost" onClick={onCancel}>
						<Trans>Cancel</Trans>
					</Button>
					<Button type="submit">{isEdit ? <Trans>Update</Trans> : <Trans>Add</Trans>}</Button>
				</DialogFooter>
			</form>
		</DialogContent>
	)
}

// ---------------------------------------------------------------------------------------------
// Field input
// ---------------------------------------------------------------------------------------------

const EMPTY_SELECT = "__empty__"

function FieldInput({
	field,
	value,
	reveal,
	error,
	onChange,
}: {
	field: Field
	value: FieldValue | undefined
	reveal: boolean
	error?: string
	onChange: (value: FieldValue) => void
}) {
	const id = `nf-${field.key}`
	const helpId = `${id}-help`
	const help = field.help?.()
	const describedBy = error || help ? helpId : undefined
	const helpText = (error || help) && (
		<p id={helpId} className={cn("text-xs", error ? "text-destructive" : "text-muted-foreground")}>
			{error ?? help}
		</p>
	)
	const label = (
		<>
			{field.label()}
			{field.required && (
				<span className="text-muted-foreground" aria-hidden="true">
					{" "}
					*
				</span>
			)}
		</>
	)

	if (field.type === "boolean") {
		return (
			<div className="grid gap-1">
				<div className="flex items-center justify-between gap-4">
					<Label htmlFor={id} className="font-normal">
						{label}
					</Label>
					<Switch id={id} checked={value === true} onCheckedChange={onChange} aria-describedby={describedBy} />
				</div>
				{helpText}
			</div>
		)
	}

	let control: React.ReactNode
	if (field.type === "select") {
		const current = typeof value === "string" && value !== "" ? value : EMPTY_SELECT
		control = (
			<Select value={current} onValueChange={(val) => onChange(val === EMPTY_SELECT ? "" : val)}>
				<SelectTrigger id={id} aria-invalid={!!error} aria-describedby={describedBy}>
					<SelectValue />
				</SelectTrigger>
				<SelectContent>
					{field.options?.map((o) => (
						<SelectItem key={o.value || EMPTY_SELECT} value={o.value || EMPTY_SELECT}>
							{o.label()}
						</SelectItem>
					))}
				</SelectContent>
			</Select>
		)
	} else if (field.type === "pairs") {
		control = (
			<Textarea
				id={id}
				rows={3}
				spellCheck={false}
				className="font-mono text-xs"
				placeholder={field.placeholder}
				value={Array.isArray(value) ? value.join("\n") : String(value ?? "")}
				aria-invalid={!!error}
				aria-describedby={describedBy}
				onChange={(e) => onChange(e.target.value)}
			/>
		)
	} else {
		const text = Array.isArray(value) ? value.join(", ") : String(value ?? "")
		control = (
			<Input
				id={id}
				type={field.type === "password" && !reveal ? "password" : "text"}
				inputMode={field.type === "number" ? "numeric" : undefined}
				autoComplete="off"
				spellCheck={false}
				placeholder={field.placeholder ?? (field.type === "list" ? t`Separate with commas` : undefined)}
				value={text}
				aria-invalid={!!error}
				aria-required={field.required}
				aria-describedby={describedBy}
				onChange={(e) => onChange(e.target.value)}
			/>
		)
	}

	return (
		<div className="grid gap-2">
			<Label htmlFor={id}>{label}</Label>
			{control}
			{helpText}
		</div>
	)
}
