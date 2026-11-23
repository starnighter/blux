<script lang="ts">
 import {onMount} from 'svelte';
 import {request,errorText} from '$lib/blux/api';
 import ComparisonPane from './ComparisonPane.svelte';
 export let taskID='';
 export let taskIDs:string[]|undefined=undefined;
 let tasks:{id:string;title:string}[]=[];let error='';
 onMount(()=>{void request<{tasks:{id:string;title:string}[]}>('/tasks').then(data=>tasks=data.tasks).catch(e=>error=errorText(e));});
 $: choices=tasks.filter(t=>!taskIDs||taskIDs.includes(t.id));
 $: initial=choices.find(t=>t.id===taskID)?.id||choices[0]?.id||'';
</script>
{#if error}<p class="inline-error" role="alert">{error}</p>{/if}
{#if choices.length}<div class="compare-grid">
 <ComparisonPane title="左侧版本" tasks={choices} initial={initial}/>
 <ComparisonPane title="右侧版本" tasks={choices} initial={choices.find(t=>t.id!==initial)?.id||initial}/>
</div>{:else if !error}<p class="workspace-list-notice">暂无可比较任务。</p>{/if}
<style>.compare-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));width:100%;height:100%;min-height:20rem;overflow:auto;gap:.5rem}@media(max-width:650px){.compare-grid{grid-template-columns:minmax(0,1fr);grid-template-rows:repeat(2,minmax(18rem,1fr))}}</style>
