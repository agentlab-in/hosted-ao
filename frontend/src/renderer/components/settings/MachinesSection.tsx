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
 * This computer is machine zero: it is always in the list and selectable.
 * Every remote entry is paired directly by address, port, passcode, and pinned
 * certificate fingerprint (docs/adr/0003-pair-mode-gateway.md).
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
	const apply = (next: AoMachinesState) => queryClient.setQueryData(aoMachinesQueryKey, next);
	const invalidatePaired = () => {
		void queryClient.invalidateQueries({ queryKey: aoPairedMachinesQueryKey });
		void queryClient.invalidateQueries({ queryKey: aoMachinesQueryKey });
	};

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
	const activeMachineId = state?.activeMachineId ?? "";
	const mutationError = select.error instanceof Error ? select.error.message : null;
	// A rejected refresh carries its reason on the query, not in `state`, which
	// is undefined in that case. Without this the pane rendered nothing at all
	// for a failed refresh, which would otherwise leave a permanent spinner.
	const refreshError = query.error instanceof Error ? query.error.message : null;
	const error = state?.error ?? refreshError ?? mutationError;
	const hasOnlyLocalMachine = state?.status === "ready" && machines.length === 1 && machines[0]?.local;

	return (
		<SettingsSection title="Machines" sectionId="machines">
			{hasOnlyLocalMachine ? (
				<div className="rounded-md bg-raised px-3 py-3" data-testid="self-hosting-empty-state">
					<p className="text-sm font-medium text-settings-label">This computer is ready for local work</p>
					<p className="mt-1 text-xs leading-row text-settings-muted">
						AO runs sessions on this computer by default. Pair another machine to run sessions on a remote box you control.
					</p>
				</div>
			) : null}

			{machines.map((machine) =>
				machine.local ? (
					<MachineRow key={machine.id} machine={machine} active={machine.id === activeMachineId}
						busy={select.isPending} onSelect={() => select.mutate(machine.id)} />
				) : (
					<PairedMachineRow key={machine.id} machine={machine} active={machine.id === activeMachineId}
						busy={select.isPending} onSelect={() => select.mutate(machine.id)}
						onRemove={() => setRemoveTarget(machine)} />
				),
			)}

			{query.isLoading ? (
				<p className="flex items-center gap-2 px-1 text-xs leading-row text-settings-muted">
					Checking machine connectivity…
				</p>
			) : null}

			<p className="px-1 text-xs leading-row text-settings-muted">
				One machine is active at a time. This computer is always available; paired machines connect directly to remote boxes you control.
			</p>

			{/* `ao doctor` is a local command with no HTTP route yet, so nothing
			    carries a paired machine's readiness here. Saying so once beats
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
 * A paired box is selectable through the same machine-selection bridge as the
 * local machine. An unreachable box remains listed with its last-seen time;
 * removal stays separate so selecting a row can never delete it.
 */
function PairedMachineRow({
	machine,
	active,
	busy,
	onSelect,
	onRemove,
}: {
	machine: AoMachine;
	active: boolean;
	busy: boolean;
	onSelect: () => void;
	onRemove: () => void;
}) {
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
				<button
					type="button"
					onClick={onSelect}
					disabled={busy || active}
					aria-pressed={active}
					className="flex shrink-0 items-center gap-(--size-settings-row-icon-gap) text-left transition-colors hover:bg-settings-menu-selected disabled:hover:bg-transparent"
				>
					<Router className="size-icon-lg shrink-0 text-settings-muted" aria-hidden="true" />
					<span className="whitespace-nowrap text-sm leading-5 text-settings-label">{machine.name}</span>
					<Badge variant="outline">{t("pairing.originPaired")}</Badge>
				</button>
				<div className="flex min-w-0 flex-1 items-center justify-end gap-2">
					<span className="min-w-0 truncate text-control text-settings-muted">{detail}</span>
					{offline ? <Badge variant="error">Offline</Badge> : null}
					{active ? (
						<Badge variant="accent">
							<Check className="size-3" aria-hidden="true" />
							Active
						</Badge>
					) : null}
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
 * A paired machine with no agent harness cannot run an agent, so it says
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
