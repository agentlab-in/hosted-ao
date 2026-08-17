import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Check, Copy, Laptop, Loader2, Plus, Router, Server, Trash2 } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { aoBridge } from "../../lib/bridge";
import { formatLastSeen, type AoMachine, type AoMachinesState } from "../../../shared/ao-machines";
import { Badge } from "../ui/badge";
import { Button } from "../ui/button";
import { ConfirmDialog } from "../ConfirmDialog";
import { AddPairedMachineDialog } from "./AddPairedMachineDialog";
import { SettingsLinkRow } from "./SettingsRow";
import { SettingsSection } from "./SettingsSection";

export const aoMachinesQueryKey = ["ao-machines"] as const;
export const aoPairedMachinesQueryKey = ["ao-paired-machines"] as const;

/**
 * The machine picker. One machine is active at a time, and switching re-points
 * the app at that machine's base URL.
 *
 * This computer is machine zero: it is always in the list, it is always
 * selectable, and it never needs an account. Everything below it is either a
 * machine registered with `ao setup-vm`, or a machine paired by address, port,
 * and passcode (docs/adr/0003-pair-mode-gateway.md). No URL and no token is
 * ever typed into this app for a registered machine; a paired machine is the
 * one exception, and its own fingerprint comparison step is what stands in for
 * a certificate authority.
 *
 * Paired machines are listed here but are not yet a selectable target: making
 * one active needs the same authenticated transport (REST bearer, /mux cookie,
 * SSE) that registered machines get from the control plane, and pair mode's
 * credential is a locally-held passcode instead, which is main-process wiring
 * this task does not build. See AddPairedMachineDialog and the PR description
 * for the exact seam.
 */
export function MachinesSection() {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	// One retry, then report. The refresh is deadlined in the main process
	// (request-deadline.ts), so a failure arrives as a rejection rather than as
	// an endless pending promise, and this pane must show it rather than spin.
	const query = useQuery({
		queryKey: aoMachinesQueryKey,
		queryFn: () => aoBridge.machines.refresh(),
		retry: 1,
	});
	const pairedQuery = useQuery({
		queryKey: aoPairedMachinesQueryKey,
		queryFn: () => aoBridge.pairedMachines.list(),
	});
	const apply = (next: AoMachinesState) => queryClient.setQueryData(aoMachinesQueryKey, next);
	const invalidatePaired = () => queryClient.invalidateQueries({ queryKey: aoPairedMachinesQueryKey });

	const select = useMutation({
		mutationFn: (machineId: string) => aoBridge.machines.select(machineId),
		onSuccess: apply,
	});
	const remove = useMutation({
		mutationFn: (id: string) => aoBridge.pairedMachines.remove(id),
		onSuccess: invalidatePaired,
	});

	const [addOpen, setAddOpen] = useState(false);
	const [removeTarget, setRemoveTarget] = useState<AoMachine | null>(null);

	const state = query.data;
	const machines = state?.machines ?? [];
	const pairedMachines = pairedQuery.data ?? [];
	const activeMachineId = state?.activeMachineId ?? "";
	const mutationError = select.error instanceof Error ? select.error.message : null;
	// A rejected refresh carries its reason on the query, not in `state`, which
	// is undefined in that case. Without this the pane rendered nothing at all
	// for a failed refresh, which is how a stalled control plane read as a
	// permanent "Looking for machines..." spinner.
	const refreshError = query.error instanceof Error ? query.error.message : null;
	const error = state?.error ?? refreshError ?? mutationError;
	const pairedError = pairedQuery.error instanceof Error ? pairedQuery.error.message : null;

	return (
		<SettingsSection title="Machines" sectionId="machines">
			{machines.map((machine) => (
				<MachineRow
					key={machine.id}
					machine={machine}
					active={machine.id === activeMachineId}
					busy={select.isPending}
					onSelect={() => select.mutate(machine.id)}
				/>
			))}

			{pairedMachines.map((machine) => (
				<PairedMachineRow key={machine.id} machine={machine} onRemove={() => setRemoveTarget(machine)} />
			))}

			{query.isLoading ? (
				<p className="flex items-center gap-2 px-1 text-xs leading-row text-settings-muted">
					<Loader2 className="size-icon-sm animate-spin" aria-hidden="true" />
					Looking for machines registered to your account…
				</p>
			) : null}

			<p className="px-1 text-xs leading-row text-settings-muted">
				{state?.status === "signed-out"
					? "Sign in to reach a machine you registered with `ao setup-vm`. This computer works without an account."
					: "One machine is active at a time. Switching points this app at that machine."}
			</p>

			{/* `ao doctor` is a local command with no HTTP route yet, so nothing
			    carries a registered machine's readiness here. Saying so once beats
			    a badge on every machine that would only mean "not asked". */}
			{machines.some((machine) => !machine.local && machine.harness === "unknown") ? (
				<p className="px-1 text-xs leading-row text-settings-muted" data-testid="ao-machines-harness-unknown">
					Agent-harness readiness is not reported for remote machines yet. Run{" "}
					<code className="font-mono">ao doctor</code> on the machine to check it.
				</p>
			) : null}

			{error ? (
				<p className="flex items-start gap-2 px-1 text-xs leading-row text-error" data-testid="ao-machines-error">
					<AlertTriangle className="mt-0.5 size-icon-sm shrink-0" aria-hidden="true" />
					<span>{error}</span>
				</p>
			) : null}

			{pairedError ? (
				<p
					className="flex items-start gap-2 px-1 text-xs leading-row text-error"
					data-testid="ao-paired-machines-error"
				>
					<AlertTriangle className="mt-0.5 size-icon-sm shrink-0" aria-hidden="true" />
					<span>{pairedError}</span>
				</p>
			) : null}

			<SettingsLinkRow icon={Plus} label={t("pairing.addMachine")} onClick={() => setAddOpen(true)} />

			<AddPairedMachineDialog open={addOpen} onOpenChange={setAddOpen} onPaired={invalidatePaired} />

			<ConfirmDialog
				open={removeTarget !== null}
				title={t("pairing.removeConfirmTitle")}
				description={t("pairing.removeConfirmBody", { name: removeTarget?.name ?? "" })}
				confirmLabel={t("pairing.remove")}
				destructive
				busy={remove.isPending}
				error={remove.error instanceof Error ? remove.error.message : null}
				onConfirm={() => {
					if (!removeTarget) return;
					remove.mutate(removeTarget.id, { onSuccess: () => setRemoveTarget(null) });
				}}
				onOpenChange={(open) => {
					if (!open) setRemoveTarget(null);
				}}
			/>
		</SettingsSection>
	);
}

function MachineRow({
	machine,
	active,
	busy,
	onSelect,
}: {
	machine: AoMachine;
	active: boolean;
	busy: boolean;
	onSelect: () => void;
}) {
	const Icon = machine.local ? Laptop : Server;
	const offline = machine.reachability === "offline";
	const lastSeen = formatLastSeen(machine.lastSeen);
	const detail = machine.local
		? "Runs on this computer"
		: offline
			? lastSeen
				? `Offline, last seen ${lastSeen}`
				: "Offline, has never connected"
			: new URL(machine.baseUrl).host;

	return (
		<div className="flex w-full flex-col gap-1.5" data-testid="ao-machine" data-machine-id={machine.id}>
			<button
				type="button"
				onClick={onSelect}
				disabled={busy || active}
				aria-pressed={active}
				className="settings-row-bar w-full text-left transition-colors hover:bg-settings-menu-selected disabled:hover:bg-transparent"
			>
				<div className="flex shrink-0 items-center gap-(--size-settings-row-icon-gap)">
					<Icon className="size-icon-lg shrink-0 text-settings-muted" aria-hidden="true" />
					<span className="whitespace-nowrap text-sm leading-5 text-settings-label">{machine.name}</span>
				</div>
				<div className="flex min-w-0 flex-1 items-center justify-end gap-2">
					<span className="min-w-0 truncate text-control text-settings-muted">{detail}</span>
					{offline ? <Badge variant="error">Offline</Badge> : null}
					{machine.harness === "missing" ? <Badge variant="warning">No harness</Badge> : null}
					{active ? (
						<Badge variant="accent">
							<Check className="size-3" aria-hidden="true" />
							Active
						</Badge>
					) : null}
				</div>
			</button>

			{machine.harness === "missing" && machine.harnessCommand ? (
				<HarnessHint machineName={machine.name} command={machine.harnessCommand} />
			) : null}
		</div>
	);
}

/**
 * A paired box (docs/adr/0003-pair-mode-gateway.md), listed alongside local
 * and hosted machines but rendered as a static row rather than a `MachineRow`
 * button: there is no bridge call yet to make a paired machine the active one
 * (see the module doc comment above), so this row only ever offers removal,
 * never a click-to-select action that would silently do nothing or select the
 * wrong transport. An unreachable paired box still renders here, with its
 * last-seen time, exactly like an offline registered machine does.
 */
function PairedMachineRow({ machine, onRemove }: { machine: AoMachine; onRemove: () => void }) {
	const { t } = useTranslation();
	const offline = machine.reachability === "offline";
	const lastSeen = formatLastSeen(machine.lastSeen);
	const detail = offline
		? lastSeen
			? `Offline, last seen ${lastSeen}`
			: "Offline, has never connected"
		: new URL(machine.baseUrl).host;

	return (
		<div className="flex w-full flex-col gap-1.5" data-testid="ao-machine" data-machine-id={machine.id}>
			<div className="settings-row-bar w-full">
				<div className="flex shrink-0 items-center gap-(--size-settings-row-icon-gap)">
					<Router className="size-icon-lg shrink-0 text-settings-muted" aria-hidden="true" />
					<span className="whitespace-nowrap text-sm leading-5 text-settings-label">{machine.name}</span>
					<Badge variant="outline">{t("pairing.originPaired")}</Badge>
				</div>
				<div className="flex min-w-0 flex-1 items-center justify-end gap-2">
					<span className="min-w-0 truncate text-control text-settings-muted">{detail}</span>
					{offline ? <Badge variant="error">Offline</Badge> : null}
					<Button type="button" variant="ghost" size="sm" onClick={onRemove}>
						<Trash2 className="size-icon-sm" aria-hidden="true" />
						{t("pairing.remove")}
					</Button>
				</div>
			</div>
		</div>
	);
}

/**
 * A registered machine with no agent harness cannot run an agent, so it says
 * exactly which command to run on it rather than failing later with nothing to
 * act on. The command runs on that machine, over SSH, not here.
 */
function HarnessHint({ machineName, command }: { machineName: string; command: string }) {
	const [copied, setCopied] = useState(false);

	return (
		<div
			className="flex items-center gap-2 rounded-md bg-raised px-3 py-2 text-xs leading-row text-settings-muted"
			data-testid="ao-machine-harness-hint"
		>
			<span className="min-w-0 flex-1">
				No agent harness on {machineName}. Run <code className="font-mono text-foreground">{command}</code> on that
				machine.
			</span>
			<Button
				type="button"
				variant="ghost"
				size="sm"
				onClick={async () => {
					await aoBridge.clipboard.writeText(command);
					setCopied(true);
				}}
			>
				{copied ? <Check className="size-icon-sm" aria-hidden="true" /> : <Copy className="size-icon-sm" aria-hidden="true" />}
				{copied ? "Copied" : "Copy"}
			</Button>
		</div>
	);
}
