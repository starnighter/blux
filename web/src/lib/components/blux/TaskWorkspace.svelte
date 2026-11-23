<script lang="ts">
 import {onMount,onDestroy} from 'svelte';import {request,errorText} from '$lib/blux/api';import {folderName} from '$lib/blux/context';
 import Icon from './Icon.svelte';import Select from './Select.svelte';import FileTree,{type Entry} from './FileTree.svelte';import WorkbenchDocument from './WorkbenchDocument.svelte';import {fileKey,type WorkbenchFile,type WorkbenchSnapshot} from '$lib/blux/workbench';
 import ArtifactComparison from './ArtifactComparison.svelte';
 import ResizeHandle from './ResizeHandle.svelte';
 export let initialSnapshot='';
 export let taskID='';export let project='';export let workspacePath='';export let busy=false;export let managed=false;export let revision=0;export let onClose:()=>void=()=>{};
 export let onVerify:((snapshotID:string)=>void)|undefined=undefined;
 export let width=600;export let maxWidth=1000;export let resetWidth=600;export let resizable=true;export let onResize:(width:number)=>void=()=>{};export let onResizeEnd:()=>void=()=>{};
 let panel:HTMLElement;let panelWidth=600;let treeWidth=196;let treeOpen=true;
 $: treeMin=panelWidth<480?104:140;
 $: treeMax=Math.max(treeMin,Math.min(480,panelWidth-220));
 $: actualTreeWidth=Math.min(treeMax,Math.max(treeMin,treeWidth));
 function saveTreeWidth(){try{localStorage.setItem('blux-file-tree-width',String(treeWidth));}catch{/* Keep session width. */}}
 type Snapshot=WorkbenchSnapshot;
 let tab:'artifacts'|'changes'|'files'=initialSnapshot?'artifacts':'files';let snapshots:Snapshot[]=[];let version=initialSnapshot||'latest';let fileVersion=initialSnapshot||'live';let expanded=false;
 let entries:Entry[]=[];let listing=false;let listError='';let notice='';let treeRevision=0;
 let docs:WorkbenchFile[]=[];let active='';let comparing=false;let savedSelection:WorkbenchFile|null=null;let selectedPaths:string[]=[];
 let alive=true;let treeEpoch=0;let snapshotEpoch=0;let scope='';let accepting=false;let acceptResult='';let acceptError='';let acceptance:{id:string;key:string}|null=null;
 $: snapshot=(tab==='files'?fileVersion:version)==='latest'?snapshots.at(-1):snapshots.find(s=>s.id===(tab==='files'?fileVersion:version));
 $: snapshotID=snapshot?.id||'';
 $: live=tab==='files'?fileVersion==='live':tab==='changes'&&version==='live';
 $: rootName=tab==='artifacts'?'产物':tab==='changes'?'变更':folderName(project)||'文件';
 $: versions=[{value:'latest',label:'最新版本'},...snapshots.map((s,i)=>({value:s.id,label:`版本 ${i+1}${s.parent_snapshot_id?' · 人工修订':''}`}))];
 $: currentScope=JSON.stringify([taskID,tab,workspacePath,snapshotID,live,version,fileVersion]);
 $: if(currentScope!==scope){scope=currentScope;entries=[];selectedPaths=[];acceptError='';acceptResult='';void loadTree();}
 $: if(taskID){void loadSnapshots(taskID,revision);}
 $: selected=docs.find(d=>fileKey(d)===active);
 $: verifyID=selected?(selected.version==='live'?'':selected.version):snapshotID;
 $: canAccept=tab==='changes'&&!!snapshot?.commit&&!!snapshot.diff&&!managed&&!live&&snapshot.id===snapshots.at(-1)?.id&&!busy;
 onMount(()=>{try{const saved=Number(localStorage.getItem('blux-file-tree-width'));if(saved>=104&&saved<=480)treeWidth=saved;}catch{/* Use default width. */}const observer=new ResizeObserver(([entry])=>panelWidth=entry.contentRect.width);observer.observe(panel);const timer=setInterval(()=>{if(busy&&live)void loadTree(true)},5000);return()=>{clearInterval(timer);observer.disconnect();}});
 onDestroy(()=>{alive=false;++treeEpoch;++snapshotEpoch;});
 async function loadSnapshots(id:string,_revision:number){const token=++snapshotEpoch;try{const result=await request<{snapshots:Snapshot[]}>(`/tasks/${id}/snapshots`);if(alive&&token===snapshotEpoch){snapshots=result.snapshots;}}catch(e){if(alive&&token===snapshotEpoch)listError=errorText(e);}}
 function artifactEntries(directory=''):Entry[]{const prefix=directory?directory+'/':'';const result=new Map<string,Entry>();for(const a of snapshot?.artifacts||[]){if(!a.path.startsWith(prefix))continue;const remaining=a.path.slice(prefix.length);const [name,...rest]=remaining.split('/');if(!name)continue;const path=prefix+name;result.set(path,rest.length?{name,path,kind:'directory'}:{...a,name,path:a.path,kind:'file'});}return [...result.values()].sort((a,b)=>(a.kind==='directory'?0:1)-(b.kind==='directory'?0:1)||a.name.localeCompare(b.name));}
 async function loadDirectory(path:string):Promise<Entry[]>{if(tab==='artifacts')return artifactEntries(path);const result=await request<{entries:Entry[];truncated?:boolean}>(`/tasks/${taskID}/files?path=${encodeURIComponent(path)}${!live&&snapshotID?`&snapshot=${snapshotID}`:''}`);return result.entries;}
 async function loadTree(preserve=false){const token=++treeEpoch;listing=true;listError='';notice='';try{
  let values:Entry[]=[];let message='';
  if(tab==='artifacts'){values=artifactEntries();}
  else if(!live&&!snapshotID){values=[];}
  else{const result=await request<{entries:Entry[];pending?:boolean;truncated?:boolean}>(`/tasks/${taskID}/files?path=${tab==='changes'?'&scope=changes':''}${!live&&snapshotID?`&snapshot=${snapshotID}`:''}`);values=result.entries;if(result.pending)message='工作区尚未准备完成。';else if(result.truncated)message='目录较大，仅显示前 500 项。';}
  if(!alive||token!==treeEpoch)return;notice=message;entries=tab==='changes'?values.map(e=>({...e,name:e.path})):values;treeRevision++;
  if(savedSelection?.version===snapshotID){savedSelection=null;return;}
  if(!preserve){const first=entries.find(e=>e.name.toLowerCase()==='readme.md')||entries.find(e=>e.kind==='file');if(first)view(first);}
 }catch(e){if(alive&&token===treeEpoch)listError=errorText(e);}finally{if(alive&&token===treeEpoch)listing=false;}}
 function view(entry:Entry){
  const doc:WorkbenchFile={task:taskID,version:live?'live':snapshotID,kind:tab==='changes'?'diff':tab==='artifacts'?'artifact':'file',entry};
  active=fileKey(doc);
  if(!docs.some(d=>fileKey(d)===active))docs=[...docs,doc];
 }
 function closeDoc(key:string){
  const index=docs.findIndex(d=>fileKey(d)===key);
  docs=docs.filter(d=>fileKey(d)!==key);
  if(active===key)active=docs.length?fileKey(docs[Math.max(0,index-1)]):'';
 }
 function saved(next:Snapshot,doc:WorkbenchFile){
  snapshots=[...snapshots.filter(s=>s.id!==next.id),next];
  const entry=doc.kind==='artifact'?next.artifacts.find(a=>a.path===doc.entry.path):doc.entry;
  if(!entry)return;
  const revised={...doc,version:next.id,entry:{...entry,name:doc.entry.name}};
  docs=[...docs,revised]; active=fileKey(revised); savedSelection=revised;
  tab=doc.kind==='artifact'?'artifacts':'files';
  if(tab==='files')fileVersion=next.id;
  if(tab==='artifacts')version=next.id;
  acceptResult='已保存为新的不可变修订。';
 }
 function togglePath(path:string){
  selectedPaths=selectedPaths.includes(path)?selectedPaths.filter(p=>p!==path):[...selectedPaths,path];
 }
 function refresh(){void loadSnapshots(taskID,revision);void loadTree(true);}
 async function accept(all:boolean){if(!canAccept||!snapshot||accepting)return;const id=snapshot.id;const paths=all?undefined:[...selectedPaths].sort();const token=JSON.stringify([id,paths]);if(acceptance?.id!==token)acceptance={id:token,key:crypto.randomUUID()};accepting=true;acceptError='';try{const result=await request<{status:string;reason:string;integration_path:string}>(`/snapshots/${id}/accept`,{method:'POST',headers:{'Idempotency-Key':acceptance.key},body:JSON.stringify({paths})});if(!alive)return;acceptResult=result.status==='accepted'?'已接收到源项目':`${result.reason} ${result.integration_path}`;if(result.status==='blocked')acceptance=null;}catch(e){if(alive)acceptError=errorText(e);}finally{if(alive)accepting=false;}}
</script>
<aside bind:this={panel} class="task-workspace-panel" class:workspace-expanded={expanded} aria-label="任务文件与产物">
 <ResizeHandle label="调整右侧工作区宽度" value={width} min={360} max={maxWidth} direction={-1} edge="left" enabled={resizable&&!expanded} resetValue={resetWidth} {onResize} onCommit={onResizeEnd}/>
 <header class="task-workspace-header"><nav aria-label="任务工作区">{#if !treeOpen}<button class="ui-icon-button" aria-label="显示文件列表" on:click={()=>treeOpen=true}><Icon name="sidebar" size={16}/></button>{/if}<button class:active={tab==='artifacts'} on:click={()=>tab='artifacts'}><Icon name="file" size={16}/>产物</button><button class:active={tab==='changes'} on:click={()=>tab='changes'}><Icon name="changes" size={16}/>变更</button><button class:active={tab==='files'} on:click={()=>tab='files'}><Icon name="folder" size={17}/>文件</button></nav><div><button class="preview-mode" aria-pressed={comparing} on:click={()=>{comparing=!comparing;if(comparing)expanded=true}}>双栏比较</button><button class="ui-icon-button workspace-expand-button" aria-label={expanded?'退出展开':'展开工作区'} on:click={()=>expanded=!expanded}><Icon name={expanded?'collapse':'expand'} size={16}/></button><button class="ui-icon-button" aria-label="关闭文件与产物" on:click={onClose}><Icon name="sidebar" size={17}/></button></div></header>
 {#if comparing}<div class="comparison-area"><ArtifactComparison {taskID}/></div>{/if}
 <div hidden={comparing} class="task-workspace-content" class:has-preview={!!selected}>
  {#if treeOpen}<section class="workspace-file-list" style={`width:${actualTreeWidth}px;`} aria-label={tab==='changes'?'变更列表':tab==='artifacts'?'产物列表':'项目文件列表'}>
   <ResizeHandle label="调整文件目录宽度" value={actualTreeWidth} min={treeMin} max={treeMax} resetValue={196} onResize={width=>treeWidth=width} onCommit={saveTreeWidth}/>
   <header><strong><button class="ui-icon-button" aria-label="收起文件列表" on:click={()=>treeOpen=false}><Icon name="sidebar" size={14}/></button><span title={rootName}>{rootName}</span></strong><button class="ui-icon-button" aria-label="刷新文件列表" on:click={refresh} disabled={listing}><Icon name="refresh" size={14}/></button></header>
   <div class="workspace-version">{#if tab==='files'}<Select ariaLabel="文件版本" bind:value={fileVersion} options={[{value:'live',label:'当前工作区'},...versions]} compact/>{:else if snapshots.length||tab==='changes'}<Select ariaLabel="产物版本" bind:value={version} options={tab==='changes'?[...versions,{value:'live',label:'当前工作区'}]:versions} disabled={accepting} compact/>{/if}</div>
   <div class="workspace-tree-scroll">
    {#if listError}<p class="workspace-list-notice" role="alert">{listError}<button on:click={()=>void loadTree()}>重试</button></p>
    {:else if listing&&!entries.length}<p class="workspace-list-notice">读取中…</p>
    {:else if !entries.length}<p class="workspace-list-notice">{notice||(tab==='artifacts'?'暂无产物':tab==='changes'?'暂无代码变更':'暂无文件')}</p>
    {:else if tab==='changes'&&canAccept}<ul class="selection-list">{#each entries as entry}<li><input type="checkbox" aria-label={`选择 ${entry.path}`} checked={selectedPaths.includes(entry.path)} on:change={()=>togglePath(entry.path)}/><button title={entry.path} on:click={()=>view(entry)}>{entry.path}</button></li>{/each}</ul>
    {:else}{#key scope}<FileTree {entries} selected={selected?.entry.path||''} revision={treeRevision} {loadDirectory} onSelect={entry=>void view(entry)}/>{/key}{#if notice}<p class="workspace-list-notice">{notice}</p>{/if}{/if}
   </div>
   {#if canAccept}<footer class="accept-buttons"><button class="workspace-accept" disabled={accepting||!selectedPaths.length} on:click={()=>void accept(false)}>接收所选（{selectedPaths.length}）</button><button class="preview-mode" disabled={accepting} on:click={()=>void accept(true)}>接收全部</button></footer>{/if}
  </section>{/if}
  <section class="documents" aria-label="打开的文件">
   {#if onVerify&&verifyID}<div class="verify-version"><button class="preview-mode" on:click={()=>onVerify?.(verifyID)}>送交验证</button></div>{/if}
   {#if docs.length}<nav class="document-tabs" aria-label="文件标签">{#each docs as doc (fileKey(doc))}
    <div class:active={fileKey(doc)===active}><button title={doc.entry.path} aria-pressed={fileKey(doc)===active} on:click={()=>active=fileKey(doc)}>{doc.entry.path.split('/').at(-1)} <small>{doc.version==='live'?'当前':`v${snapshots.findIndex(s=>s.id===doc.version)+1}`}{doc.kind==='diff'?' · Diff':''}</small></button><button aria-label={`关闭 ${doc.entry.path}`} on:click={()=>closeDoc(fileKey(doc))}>×</button></div>
   {/each}</nav>{/if}
   {#each docs as doc (fileKey(doc))}
    <div class="document-pane" hidden={active!==fileKey(doc)}>
     <WorkbenchDocument file={doc} editable={!busy&&doc.version===snapshots.at(-1)?.id} refresh={doc.version==='live'?treeRevision:0} onSaved={next=>saved(next,doc)}/>
    </div>
   {:else}<div class="workspace-preview-empty"><Icon name="file" size={30}/><p>选择文件查看内容</p></div>{/each}
  </section>
 </div>
 {#if acceptResult||acceptError}<p class="workspace-accept-result" role={acceptError?'alert':'status'}>{acceptError||acceptResult}</p>{/if}
</aside>

<style>
.task-workspace-content[hidden]{display:none}.comparison-area{flex:1;min-height:0}
.verify-version{align-self:flex-end;padding:.25rem .45rem}
.documents{display:flex;flex:1;min-width:0;min-height:0;flex-direction:column}
.document-tabs{display:flex;flex:none;align-items:stretch;overflow-x:auto;border-bottom:1px solid var(--border)}
.document-tabs>div{display:flex;align-items:center;white-space:nowrap;border-bottom:2px solid transparent}
.document-tabs>div.active{border-bottom-color:currentColor}
.document-tabs button{font-size:.78rem;padding:.45rem .3rem}.document-tabs small{opacity:.65}
.document-pane{display:flex;flex:1;min-height:0;min-width:0}.document-pane[hidden]{display:none}
.selection-list{list-style:none;padding:.35rem;margin:0}.selection-list li{display:flex;gap:.3rem;align-items:center}
.selection-list button{padding:.4rem .25rem;overflow:hidden;white-space:nowrap;text-overflow:ellipsis}
.accept-buttons{display:flex;gap:.3rem;flex-wrap:wrap}
</style>
