import { t } from "@lingui/core/macro"
import { Trans } from "@lingui/react/macro"
import { BellIcon, LoaderCircleIcon, PlusIcon, SaveIcon } from "lucide-react"
import { useEffect, useState } from "react"
import * as v from "valibot"
import { prependBasePath } from "@/components/router"
import { Button } from "@/components/ui/button"
import { Dialog } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { InputTags } from "@/components/ui/input-tags"
import { Label } from "@/components/ui/label"
import { Separator } from "@/components/ui/separator"
import { toast } from "@/components/ui/use-toast"
import { parseReminderMinutes, REMINDER_MAX_MINUTES, REMINDER_MIN_MINUTES } from "@/lib/alert-ack"
import { isAdmin } from "@/lib/api"
import type { UserSettings } from "@/types"
import { saveSettings } from "./layout"
import { NotificationCard, NotificationDialog, WebhookUrlSchema } from "./notification-builder"
import { QuietHours } from "./quiet-hours"

const NotificationSchema = v.object({
	emails: v.array(v.pipe(v.string(), v.rfcEmail())),
	webhooks: v.array(WebhookUrlSchema),
})

const sameList = (a: string[], b: string[]) => a.length === b.length && a.every((x, i) => x === b[i])

const SettingsNotificationsPage = ({ userSettings }: { userSettings: UserSettings }) => {
	const [webhooks, setWebhooks] = useState(userSettings.webhooks ?? [])
	const [emails, setEmails] = useState<string[]>(userSettings.emails ?? [])
	const [reminder, setReminder] = useState(String(userSettings.reminderMinutes || ""))
	const reminderMinutes = parseReminderMinutes(reminder)
	const [isLoading, setIsLoading] = useState(false)
	// index of the entry being edited, "new" when adding, null when the dialog is closed
	const [editing, setEditing] = useState<number | "new" | null>(null)
	const [dialogKey, setDialogKey] = useState(0)

	// update values when userSettings changes
	useEffect(() => {
		setWebhooks(userSettings.webhooks ?? [])
		setEmails(userSettings.emails ?? [])
		setReminder(String(userSettings.reminderMinutes || ""))
	}, [userSettings])

	const isDirty =
		!sameList(webhooks, userSettings.webhooks ?? []) ||
		!sameList(emails, userSettings.emails ?? []) ||
		reminderMinutes !== (userSettings.reminderMinutes || 0)

	function openDialog(target: number | "new") {
		setDialogKey((k) => k + 1)
		setEditing(target)
	}

	function submitWebhook(url: string) {
		if (editing === "new") setWebhooks([...webhooks, url])
		else if (editing !== null) setWebhooks(webhooks.map((w, i) => (i === editing ? url : w)))
		setEditing(null)
	}

	const removeWebhook = (index: number) => setWebhooks(webhooks.filter((_, i) => i !== index))

	async function updateSettings() {
		if (reminderMinutes === null) {
			toast({
				title: t`Failed to save settings`,
				description: t`Enter a reminder interval between ${REMINDER_MIN_MINUTES} and ${REMINDER_MAX_MINUTES} minutes, or leave it blank.`,
				variant: "destructive",
			})
			return
		}
		setIsLoading(true)
		try {
			const parsedData = v.parse(NotificationSchema, { emails, webhooks })
			await saveSettings({ ...parsedData, reminderMinutes })
		} catch (e: unknown) {
			toast({
				title: t`Failed to save settings`,
				description: (e as Error).message,
				variant: "destructive",
			})
		}
		setIsLoading(false)
	}

	return (
		<div>
			<div>
				<h3 className="text-xl font-medium mb-2">
					<Trans>Notifications</Trans>
				</h3>
				<p className="text-sm text-muted-foreground leading-relaxed">
					<Trans>Configure how you receive alert notifications.</Trans>
				</p>
				<p className="text-sm text-muted-foreground mt-1.5 leading-relaxed">
					<Trans>
						Looking instead for where to create alerts? Click the bell <BellIcon className="inline h-4 w-4" /> icons in
						the systems table.
					</Trans>
				</p>
			</div>
			<Separator className="my-4" />
			<div className="space-y-5">
				<div className="grid gap-2">
					<div className="mb-2">
						<h3 className="mb-1 text-lg font-medium">
							<Trans>Email notifications</Trans>
						</h3>
						{isAdmin() && (
							<p className="text-sm text-muted-foreground leading-relaxed">
								<Trans>
									Please{" "}
									<a href={prependBasePath("/_/#/settings/mail")} className="link" target="_blank">
										configure an SMTP server
									</a>{" "}
									to ensure alerts are delivered.
								</Trans>
							</p>
						)}
					</div>
					<Label className="block" htmlFor="email">
						<Trans>To email(s)</Trans>
					</Label>
					<InputTags
						value={emails}
						onChange={setEmails}
						placeholder={t`Enter email address...`}
						className="w-full"
						type="email"
						id="email"
					/>
					<p className="text-[0.8rem] text-muted-foreground">
						<Trans>Save address using enter key or comma. Leave blank to disable email notifications.</Trans>
					</p>
				</div>
				<Separator />
				<div className="space-y-3">
					<div className="grid grid-cols-1 sm:flex items-center justify-between gap-4">
						<div>
							<h3 className="mb-1 text-lg font-medium">
								<Trans>Webhook / Push notifications</Trans>
							</h3>
							<p className="text-sm text-muted-foreground leading-relaxed">
								<Trans>
									InfraScope uses{" "}
									<a href="https://beszel.dev/guide/notifications" target="_blank" className="link" rel="noopener">
										Shoutrrr
									</a>{" "}
									to integrate with popular notification services.
								</Trans>
							</p>
						</div>
						<Button type="button" variant="outline" className="h-10 shrink-0" onClick={() => openDialog("new")}>
							<PlusIcon className="size-4" />
							<span className="ms-1">
								<Trans>Add notification</Trans>
							</span>
						</Button>
					</div>
					{webhooks.length > 0 ? (
						<div className="grid gap-2.5" id="webhooks">
							{webhooks.map((webhook, index) => (
								<NotificationCard
									key={`${index}-${webhook}`}
									url={webhook}
									onEdit={() => openDialog(index)}
									onDelete={() => removeWebhook(index)}
								/>
							))}
						</div>
					) : (
						<p className="rounded-md border border-dashed p-4 text-center text-sm text-muted-foreground">
							<Trans>No webhook or push notifications configured.</Trans>
						</p>
					)}
					<Dialog open={editing !== null} onOpenChange={(open) => !open && setEditing(null)}>
						{editing !== null && (
							<NotificationDialog
								key={dialogKey}
								url={editing === "new" ? null : (webhooks[editing] ?? null)}
								onSubmit={submitWebhook}
								onCancel={() => setEditing(null)}
							/>
						)}
					</Dialog>
				</div>
				<Separator />
				<div className="grid gap-2">
					<div className="mb-1">
						<h3 className="mb-1 text-lg font-medium">
							<Trans>Reminders</Trans>
						</h3>
						<p className="text-sm text-muted-foreground leading-relaxed">
							<Trans>
								Repeat notifications for unacknowledged alerts every N minutes until they are acknowledged or resolved
								(at most 24 times). Quiet hours apply.
							</Trans>
						</p>
					</div>
					<Label className="block" htmlFor="reminder-minutes">
						<Trans>Reminder interval (minutes)</Trans>
					</Label>
					<Input
						id="reminder-minutes"
						type="number"
						inputMode="numeric"
						min={REMINDER_MIN_MINUTES}
						max={REMINDER_MAX_MINUTES}
						step={1}
						value={reminder}
						onChange={(e) => setReminder(e.target.value)}
						placeholder={t`Off`}
						className="w-40"
						aria-invalid={reminderMinutes === null}
					/>
					<p
						className={
							reminderMinutes === null ? "text-[0.8rem] text-destructive" : "text-[0.8rem] text-muted-foreground"
						}
					>
						<Trans>
							Between {REMINDER_MIN_MINUTES} and {REMINDER_MAX_MINUTES}. Leave blank or 0 to disable reminders.
						</Trans>
					</p>
				</div>
				<Separator />
				<div className="space-y-3">
					<QuietHours />
				</div>
				<Separator />
				<div className="flex flex-wrap items-center gap-x-4 gap-y-2">
					<Button
						type="button"
						className="flex items-center gap-1.5 disabled:opacity-100"
						onClick={updateSettings}
						disabled={isLoading}
					>
						{isLoading ? <LoaderCircleIcon className="h-4 w-4 animate-spin" /> : <SaveIcon className="h-4 w-4" />}
						<Trans>Save Settings</Trans>
					</Button>
					{isDirty && (
						<output className="text-sm text-muted-foreground">
							<Trans>You have unsaved changes.</Trans>
						</output>
					)}
				</div>
			</div>
		</div>
	)
}

export default SettingsNotificationsPage
