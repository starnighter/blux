<script lang="ts">
	import {onMount} from 'svelte';
	import {errorText} from '$lib/blux/api';
	export let url: string;
	export let name: string;
	let objectURL = '';
	let error = '';
	onMount(() => {
		const abort = new AbortController();
		async function load() {
			const response = await fetch(url, {signal:abort.signal});
			if (!response.ok) throw new Error('无法打开 PDF 文件。');
			const blob = await response.blob();
			if ((await blob.slice(0,5).text()) !== '%PDF-') throw new Error('此文件缺少 PDF 文件头。');
			if (!abort.signal.aborted) objectURL = URL.createObjectURL(blob.slice(0, blob.size, 'application/pdf'));
		}
		void load().catch(e => {if (!abort.signal.aborted) error = errorText(e);});
		return () => {abort.abort(); URL.revokeObjectURL(objectURL);};
	});
</script>
{#if error}<p role="alert">{error}</p>
{:else if objectURL}<iframe title={name} src={objectURL} class="pdf-document"></iframe>
{:else}<p role="status">正在加载 PDF…</p>{/if}
<style>.pdf-document{display:block;width:100%;height:100%;min-height:22rem;border:0}</style>
