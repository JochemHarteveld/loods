<script lang="ts">
	import { Terminal } from '@xterm/xterm';
	import { FitAddon } from '@xterm/addon-fit';
	import '@xterm/xterm/css/xterm.css';

	let { id }: { id: string } = $props();
	// Props are getters: reading `id` in the effect would also subscribe it to the
	// parent's whole proc object, rebuilding the terminal on every procs update.
	// A derived only changes when the id string does.
	const procKey = $derived(id);

	let el: HTMLDivElement;
	let term: Terminal | undefined;

	export function focus() {
		term?.focus();
	}

	// One terminal + websocket per process. The server replays the backlog on
	// connect, so switching processes or reconnecting shows the full history.
	$effect(() => {
		const procId = procKey;
		const css = getComputedStyle(document.documentElement);
		const v = (name: string) => css.getPropertyValue(name).trim();
		const t = new Terminal({
			fontFamily: "'JetBrains Mono', 'Fira Code', ui-monospace, Menlo, monospace",
			fontSize: 12.5,
			lineHeight: 1.15,
			scrollback: 10000,
			allowProposedApi: false,
			theme: { background: v('--term-bg'), foreground: v('--term-fg'), cursor: v('--accent'), selectionBackground: v('--term-sel') }
		});
		const fit = new FitAddon();
		t.loadAddon(fit);
		t.open(el);
		fit.fit();
		term = t;

		let ws: WebSocket | undefined;
		let closed = false;
		let retry: ReturnType<typeof setTimeout> | undefined;
		const send = (msg: object) => ws?.readyState === WebSocket.OPEN && ws.send(JSON.stringify(msg));
		const connect = () => {
			const proto = location.protocol === 'https:' ? 'wss' : 'ws';
			ws = new WebSocket(`${proto}://${location.host}/api/procs/term?id=${encodeURIComponent(procId)}`);
			ws.binaryType = 'arraybuffer';
			ws.onopen = () => {
				// Reset in the write queue rather than now, so the old screen stays up
				// until the backlog replaces it in the same pass instead of flashing blank.
				t.write('\x1bc');
				send({ type: 'resize', cols: t.cols, rows: t.rows });
			};
			ws.onmessage = (e) => t.write(new Uint8Array(e.data as ArrayBuffer));
			// Restarts replace the process server-side and close this socket: reattach.
			ws.onclose = () => {
				if (!closed) retry = setTimeout(connect, 800);
			};
		};
		connect();

		const onData = t.onData((data) => send({ type: 'input', data }));
		const onResize = t.onResize(({ cols, rows }) => send({ type: 'resize', cols, rows }));
		const ro = new ResizeObserver(() => fit.fit());
		ro.observe(el);

		return () => {
			closed = true;
			clearTimeout(retry);
			ro.disconnect();
			onData.dispose();
			onResize.dispose();
			ws?.close();
			t.dispose();
			term = undefined;
		};
	});
</script>

<div class="term" bind:this={el}></div>

<style>
	.term {
		width: 100%;
		height: 100%;
		background: var(--term-bg);
		padding: 8px 0 0 10px;
	}
	.term :global(.xterm-viewport) {
		background: var(--term-bg) !important;
	}
</style>
