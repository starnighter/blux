<script lang="ts">
 import {onDestroy} from 'svelte';
 import FilePreview from './FilePreview.svelte';
 import {request,errorText} from '$lib/blux/api';
 import type {WorkbenchFile,WorkbenchSnapshot} from '$lib/blux/workbench';
 export let file:WorkbenchFile;
 export let editable=false;
 export let refresh=0;
 export let onSaved:(revision:WorkbenchSnapshot)=>void=()=>{};
 let content='';let binary=false;let truncated=false;let loading=false;let error='';let epoch=0;
 $: immutable=file.version!=='live';
 $: fileURL=`/tasks/${file.task}/file?path=${encodeURIComponent(file.entry.path)}${immutable?'&snapshot='+encodeURIComponent(file.version):''}`;
 $: previewURL=file.kind==='artifact'?`/api/v1/previews/${file.version}/`+encoded(file.entry.path):`/api/v1/tasks/${file.task}/previews/${file.version}/`+encoded(file.entry.path);
 $: downloadURL=file.kind==='artifact'?`/api/v1/artifacts/${file.entry.id}/content`:file.entry.change==='deleted'?'':`/api/v1${fileURL}&raw=1&download=1`;
 $: void load(file,fileURL,refresh);
 function encoded(name:string){return name.split('/').map(encodeURIComponent).join('/');}
 onDestroy(()=>{++epoch});
 async function load(active:WorkbenchFile,url:string,_revision:number){
  const token=++epoch;
  loading=true;error='';binary=false;truncated=false;content='';
  try{
   if(active.kind==='artifact'){
    if((active.entry.size||0)>1<<20){binary=true;return;}
    const response=await fetch(`/api/v1/artifacts/${active.entry.id}/content`,{signal:AbortSignal.timeout(12000)});
    if(!response.ok)throw new Error('无法读取产物。');
    const bytes=new Uint8Array(await response.arrayBuffer());
    if(token!==epoch)return;
    binary=bytes.includes(0);
    if(!binary){try{content=new TextDecoder('utf-8',{fatal:true}).decode(bytes);}catch{binary=true;}}
    return;
   }
   const previous=active.entry.previous_path;
   const response=await request<{content:string;binary:boolean;truncated:boolean}>(url+(active.kind==='diff'?'&diff=1'+(previous?'&previous_path='+encodeURIComponent(previous):''):''));
   if(token!==epoch)return;
   content=response.content;binary=response.binary;truncated=response.truncated;
  }catch(e){if(token===epoch)error=errorText(e);}finally{if(token===epoch)loading=false;}
 }
</script>
<FilePreview filename={file.entry.path} {content} {error} {loading} {binary} {truncated} {editable} {previewURL} {downloadURL} {onSaved}
 snapshotID={immutable?file.version:''} kind={file.kind==='artifact'?'artifact':'file'} diff={file.kind==='diff'}
 previousPath={file.entry.previous_path||''} preferReading={file.kind==='artifact'} onRetry={()=>void load(file,fileURL,refresh)}/>
