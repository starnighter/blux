<script lang="ts">
 import Select from './Select.svelte';
 import WorkbenchDocument from './WorkbenchDocument.svelte';
 import {request,errorText} from '$lib/blux/api';
 import type {WorkbenchSnapshot,WorkbenchFile} from '$lib/blux/workbench';
 export let title:string;
 export let tasks:{id:string;title:string}[];
 export let initial:string;
 let task=initial;let snapshotID='';let artifactID='';
 let versions:WorkbenchSnapshot[]=[];let error='';let loading=false;let epoch=0;
 $: void load(task);
 $: snapshot=versions.find(v=>v.id===snapshotID);
 $: if(!snapshot?.artifacts.some(a=>a.id===artifactID))artifactID=snapshot?.artifacts[0]?.id||'';
 $: artifact=snapshot?.artifacts.find(a=>a.id===artifactID);
 $: doc=artifact?{task,version:snapshotID,kind:'artifact',entry:artifact} satisfies WorkbenchFile:null;
 async function load(id:string) {
  const token=++epoch;error='';loading=true;versions=[];snapshotID='';artifactID='';
  try{
   const result=await request<{snapshots:WorkbenchSnapshot[]}>(`/tasks/${id}/snapshots`);
   if(token!==epoch)return;
   versions=result.snapshots;snapshotID=versions.at(-1)?.id||'';
  }catch(e){if(token===epoch)error=errorText(e);}finally{if(token===epoch)loading=false;}
 }
</script>
<section class="comparison-pane" aria-label={title}>
 <header><strong>{title}</strong>
  <Select ariaLabel={title+'任务'} options={tasks.map(t=>({value:t.id,label:t.title}))} bind:value={task} compact/>
  {#if versions.length}<Select ariaLabel={title+'快照'} options={versions.map((v,i)=>({value:v.id,label:`v${i+1}${v.parent_snapshot_id?' · 人工修订':''}`}))} bind:value={snapshotID} compact/>{/if}
  {#if snapshot?.artifacts.length}<Select ariaLabel={title+'文件'} options={snapshot.artifacts.map(a=>({value:a.id,label:a.path}))} bind:value={artifactID} compact/>{/if}
 </header>
 {#if error}<p role="alert" class="inline-error">{error}</p>{:else if doc}{#key artifactID}<WorkbenchDocument file={doc}/>{/key}
 {:else}<p class="workspace-list-notice">{loading?'读取中…':'此版本暂无产物。'}</p>{/if}
</section>
<style>.comparison-pane{display:flex;flex-direction:column;border:1px solid var(--border);min-width:0;min-height:0;overflow:hidden}.comparison-pane>header{display:flex;align-items:center;gap:.3rem;padding:.4rem;flex-wrap:wrap}.comparison-pane>header strong{font-size:.8rem}</style>
