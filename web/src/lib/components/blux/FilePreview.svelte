<script lang="ts">
 import Icon from './Icon.svelte';
 import Markdown from './Markdown.svelte';
 import PdfPreview from './PdfPreview.svelte';
 import {request,errorText} from '$lib/blux/api';
 import {linesWithLocation,type TextLine,type WorkbenchSnapshot} from '$lib/blux/workbench';
 export let filename=''; export let content=''; export let loading=false; export let error=''; export let binary=false; export let truncated=false;
 export let diff=false; export let downloadURL=''; export let previewURL=''; export let preferReading=false;
 export let snapshotID=''; export let kind:'file'|'artifact'='file'; export let previousPath=''; export let editable=false;
 export let onSaved:(revision:WorkbenchSnapshot)=>void=()=>{};
 export let onRetry:()=>void=()=>{};
 type Comment={id:string;line:number;side:'new'|'old';body:string;path:string};
 let mode:'code'|'preview'|'edit'=preferReading?'preview':'code';
 let draft='';let saving=false;let saveError='';let copied=false;let copyFailed=false;
 let comments:Comment[]=[];let anchor:TextLine|null=null;let commentBody='';let posting=false;let commentError='';let commentsKey='';
 $: ext=filename.split('.').at(-1)?.toLowerCase()||'';
 $: isMarkdown=['md','markdown','mdx'].includes(ext);
 $: isImage=['png','jpg','jpeg','webp','gif','svg','avif'].includes(ext);
 $: isHTML=['html','htm'].includes(ext);
 $: editableText=['md','markdown','html','htm'].includes(ext);
 $: lines=linesWithLocation(content,diff);
 $: basename=filename.split('/').at(-1)||'';
 $: directory=filename.includes('/')?filename.slice(0,filename.lastIndexOf('/')):'';
 $: commentScope=JSON.stringify([snapshotID,kind,filename,previousPath]);
 $: if(commentScope!==commentsKey){commentsKey=commentScope;void loadComments(commentScope,snapshotID,kind,filename,previousPath);}
 async function loadComments(key:string,id:string,type:string,name:string,oldPath:string){
  comments=[];commentError='';
  if(!id||!name)return;
  try{
   const values=await Promise.all([...new Set([name,oldPath].filter(Boolean))].map(path=>request<{comments:Comment[]}>(`/snapshots/${id}/comments?kind=${type}&path=${encodeURIComponent(path)}`)));
   if(key===commentsKey)comments=values.flatMap(v=>v.comments);
  }catch(e){if(key===commentsKey)commentError=errorText(e);}
 }
 async function comment(){
  if(!anchor||!commentBody.trim()||posting)return;
  posting=true;commentError='';
  try{
   const saved=await request<Comment>(`/snapshots/${snapshotID}/comments`,{method:'POST',body:JSON.stringify({kind,path:anchor.side==='old'&&previousPath?previousPath:filename,side:anchor.side,line:anchor.line,body:commentBody.trim()})});
   comments=[...comments,saved];commentBody='';anchor=null;
  }catch(e){commentError=errorText(e);}finally{posting=false;}
 }
 async function save(){
  if(saving||draft===content)return;
  saving=true;saveError='';
  try{
   const revision=await request<WorkbenchSnapshot>(`/snapshots/${snapshotID}/revisions`,{method:'POST',body:JSON.stringify({kind,path:filename,content:draft})});
   mode='preview';onSaved(revision);
  }catch(e){saveError=errorText(e);}finally{saving=false;}
 }
 async function copy(){try{await navigator.clipboard.writeText(content);copied=true;copyFailed=false;}catch{copyFailed=true;}}
 function edit(){mode='edit';draft=content;saveError='';}
 function draftHTML(value:string,url:string){
  const address=new URL(url,location.origin);
  const base=address.href.replaceAll('&','&amp;').replaceAll('"','&quot;');
  return `<!doctype html><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline' ${address.origin}; img-src ${address.origin} data:; form-action 'none'; base-uri ${address.origin}"><base href="${base}">${value}`;
 }
</script>
<section class="workspace-file-preview" aria-label="文件预览">
 {#if filename}
  <header class="workspace-file-header"><div class="workspace-file-name" title={filename}><strong>{basename}</strong>{#if directory}<span>{directory}</span>{/if}</div><div class="workspace-file-actions">
   {#if mode==='edit'}
    <button class="preview-mode" disabled={saving} on:click={()=>mode='code'}>取消</button><button class="preview-mode" disabled={saving||draft===content} on:click={()=>void save()}>{saving?'保存中…':'保存新修订'}</button>
   {:else}
    {#if editable&&editableText&&!binary&&!truncated&&!loading&&!error&&!diff}<button class="preview-mode" on:click={edit}>编辑</button>{/if}
    {#if !diff&&(isMarkdown||isHTML)}<button class="preview-mode" on:click={()=>mode=mode==='code'?'preview':'code'}>{mode==='code'?'预览':'源码'}</button>{/if}
    {#if !binary&&!isImage}<button class="ui-icon-button" aria-label={copied?'已复制文件内容':'复制文件内容'} title={copyFailed?'复制失败，请手动选择内容':'复制内容'} disabled={loading||!!error} on:click={()=>void copy()}><Icon name={copied?'check':'copy'} size={15}/></button>{/if}
   {/if}
   {#if downloadURL}<a class="ui-icon-button" href={downloadURL} download={basename} data-sveltekit-reload data-sveltekit-preload-data="off" aria-label="下载文件"><Icon name="download" size={15}/></a>{/if}
  </div></header>
 {/if}
 {#if saveError}<p role="alert" class="inline-error">{saveError}</p>{/if}
 <div class="workspace-preview-body" class:document-preview={!diff&&mode==='preview'&&isMarkdown}>
  {#if !filename}<div class="workspace-preview-empty"><Icon name="file" size={30}/><p>选择文件查看内容</p></div>
  {:else if loading}<div class="workspace-preview-empty"><p>正在读取…</p></div>
  {:else if error}<div class="workspace-preview-empty" role="alert"><p>{error}</p><button class="subtle-button" on:click={onRetry}>重试</button></div>
  {:else if mode==='edit'}
   <div class="edit-split"><textarea aria-label="文档编辑器" bind:value={draft} spellcheck="false"></textarea><div class="draft-preview" aria-label="实时预览">{#if isMarkdown}<Markdown content={draft}/>{:else}<iframe title="HTML 实时预览" sandbox="" srcdoc={draftHTML(draft,previewURL)}></iframe>{/if}</div></div>
  {:else if isImage&&!diff&&previewURL}<div class="workspace-image-preview"><img src={previewURL} alt={basename}/></div>
  {:else if ext==='pdf'&&!diff&&previewURL}{#key previewURL}<PdfPreview url={previewURL} name={filename}/>{/key}
  {:else if mode==='preview'&&isHTML&&!diff&&previewURL}<iframe title={filename} src={previewURL} sandbox="allow-scripts" class="workspace-html-preview"></iframe>
  {:else if binary}<div class="workspace-preview-empty"><Icon name="file" size={30}/><p>此文件暂不支持预览</p>{#if downloadURL}<a class="subtle-button" href={downloadURL} download={basename}>下载文件</a>{/if}</div>
  {:else if mode==='preview'&&isMarkdown&&!diff}<Markdown {content}/>
  {:else}<div class="workspace-code" class:workspace-diff={diff} aria-label={diff?'文件变更内容':'文件内容'}>
   {#each lines.slice(0,10000) as row,index}
    <div class="workspace-code-line" class:diff-add={diff&&row.text.startsWith('+')&&!row.text.startsWith('+++')} class:diff-delete={diff&&row.text.startsWith('-')&&!row.text.startsWith('---')} class:diff-hunk={diff&&row.text.startsWith('@@')}>
     {#if snapshotID&&row.line&&!truncated}<button class="workspace-line-number line-anchor" aria-label={`评论第 ${row.line} 行`} on:click={()=>{anchor=row;commentBody=''}}>{row.line}</button>
     {:else}<span class="workspace-line-number" aria-hidden="true">{row.line||(!diff?index+1:'')}</span>{/if}
     <code>{row.text||' '}</code>
    </div>
    {#each comments.filter(c=>c.line===row.line&&c.side===row.side&&(c.path===filename||c.path===previousPath)) as c}<p class="line-note">{c.body}</p>{/each}
    {#if row===anchor}<form class="line-note" on:submit|preventDefault={()=>void comment()}><label>第 {row.line} 行评论<textarea bind:value={commentBody} maxlength="4000" rows="2"></textarea></label><button disabled={posting||!commentBody.trim()}>提交评论</button><button type="button" on:click={()=>anchor=null}>取消</button></form>{/if}
   {/each}
  </div>{/if}
 </div>
 {#if commentError}<p class="inline-error" role="alert">{commentError}</p>{/if}
 {#if truncated||lines.length>10000}<p class="workspace-preview-notice">{truncated?'仅显示前 1 MiB':'仅显示前 10,000 行'}，请下载查看完整文件。</p>{/if}
</section>
<style>
 .edit-split{display:grid;grid-template-rows:1fr 1fr;height:100%;min-height:20rem}
 .edit-split>textarea,.line-note textarea{width:100%;box-sizing:border-box;background:var(--surface);color:inherit;padding:.7rem;font:inherit;border:1px solid var(--border);resize:vertical}
 .draft-preview{overflow:auto;padding:.6rem} .draft-preview iframe{border:0;height:100%;width:100%;background:white}
 .line-note{white-space:pre-wrap;margin:.4rem 1rem .5rem 3rem;padding:.45rem .6rem;border-left:2px solid currentColor;font:normal .85rem/1.4 system-ui}
 .line-note button{margin-right:.7rem} .line-anchor{cursor:pointer} .line-anchor:hover{text-decoration:underline}
</style>
