import { t } from "@lingui/core/macro"
import { Trans } from "@lingui/react/macro"
import { EyeIcon, LoaderCircleIcon } from "lucide-react"
import type { ClientResponseError } from "pocketbase"
import { useState } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { pb } from "@/lib/api"
import { TEMPLATE_FUNCS, TEMPLATE_MAX_CHARS, TEMPLATE_VARIABLES, templateTooLong } from "@/lib/notification-channels"
import type { NotificationTemplate } from "@/types"

interface PreviewResult {
	title: string
	body: string
}

/** Renders a template with sample data on the hub. Throws with the hub's message when invalid. */
export function previewTemplate(template: NotificationTemplate): Promise<PreviewResult> {
	return pb.send<PreviewResult>("/api/beszel/notification-templates/preview", {
		method: "POST",
		body: { title: template.title ?? "", body: template.body ?? "" },
	})
}

/** Help listing the template variables and functions. */
export function TemplateVariablesHelp() {
	return (
		<div className="text-xs text-muted-foreground leading-relaxed">
			<p>
				<Trans>
					Templates use Go template syntax. Leave a field blank to use the built-in message. Available variables:
				</Trans>
			</p>
			<p className="mt-1 flex flex-wrap gap-1">
				{TEMPLATE_VARIABLES.map((name) => (
					<code key={name} className="rounded bg-muted px-1 py-0.5 font-mono">{`{{.${name}}}`}</code>
				))}
			</p>
			<p className="mt-1">
				<Trans>Functions:</Trans> <span className="font-mono">{TEMPLATE_FUNCS.join(", ")}</span>
			</p>
		</div>
	)
}

/** Title and body template fields with a preview rendered by the hub. */
export function TemplateEditor({
	idPrefix,
	value,
	onChange,
	disabled,
	titlePlaceholder,
	bodyPlaceholder,
}: {
	idPrefix: string
	value: NotificationTemplate
	onChange: (value: NotificationTemplate) => void
	disabled?: boolean
	titlePlaceholder?: string
	bodyPlaceholder?: string
}) {
	const [preview, setPreview] = useState<PreviewResult | null>(null)
	const [error, setError] = useState<string | null>(null)
	const [loading, setLoading] = useState(false)
	const tooLong = templateTooLong(value)

	async function runPreview() {
		setLoading(true)
		setError(null)
		try {
			setPreview(await previewTemplate(value))
		} catch (e) {
			setPreview(null)
			setError((e as ClientResponseError).data?.message || (e as Error).message || t`Invalid template`)
		}
		setLoading(false)
	}

	return (
		<div className="grid gap-3">
			<div className="grid gap-1.5">
				<Label htmlFor={`${idPrefix}-title`}>
					<Trans>Title template</Trans>
				</Label>
				<Input
					id={`${idPrefix}-title`}
					value={value.title ?? ""}
					maxLength={TEMPLATE_MAX_CHARS}
					placeholder={titlePlaceholder ?? "{{.Title}}"}
					spellCheck={false}
					className="font-mono text-xs"
					disabled={disabled}
					aria-invalid={tooLong.includes("title")}
					onChange={(e) => onChange({ ...value, title: e.target.value })}
				/>
			</div>
			<div className="grid gap-1.5">
				<Label htmlFor={`${idPrefix}-body`}>
					<Trans>Body template</Trans>
				</Label>
				<Textarea
					id={`${idPrefix}-body`}
					rows={4}
					value={value.body ?? ""}
					maxLength={TEMPLATE_MAX_CHARS}
					placeholder={bodyPlaceholder ?? "[{{.Severity | upper}}] {{.Message}}\n{{.Link}}"}
					spellCheck={false}
					className="font-mono text-xs"
					disabled={disabled}
					aria-invalid={tooLong.includes("body")}
					onChange={(e) => onChange({ ...value, body: e.target.value })}
				/>
			</div>
			<TemplateVariablesHelp />
			<div className="grid gap-2">
				<Button
					type="button"
					variant="outline"
					size="sm"
					className="w-fit"
					disabled={loading || disabled}
					onClick={runPreview}
				>
					{loading ? <LoaderCircleIcon className="size-4 animate-spin" /> : <EyeIcon className="size-4" />}
					<span className="ms-1">
						<Trans>Preview</Trans>
					</span>
				</Button>
				{error && (
					<p className="text-xs text-destructive" role="alert">
						{error}
					</p>
				)}
				{preview && (
					<div className="rounded-md border bg-muted/40 p-3 text-sm" aria-live="polite">
						<p className="font-medium break-words">{preview.title}</p>
						<p className="mt-1 whitespace-pre-wrap break-words text-muted-foreground">{preview.body}</p>
					</div>
				)}
			</div>
		</div>
	)
}
