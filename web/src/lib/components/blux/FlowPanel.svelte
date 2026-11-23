<script lang="ts">
  import { onMount } from 'svelte';
  import { request, errorText } from '$lib/blux/api';
  import { agentName, type AgentInfo } from '$lib/blux/agents';
  import type { SharedSnapshot } from '$lib/blux/collaborations';
  import { folderName } from '$lib/blux/context';
  import Select from './Select.svelte';

  export let project = '';
  export let contextIDs: string[] = [];
  export let collaborationID = '';
  export let inputSnapshot = '';
  export let startNew = false;
  export let snapshots: SharedSnapshot[] = [];
  export let initialGoal = '';
  export let agents: AgentInfo[] = [];
  export let onSelectProject: () => void = () => {};
  export let onTask: (id: string, snapshot?: string) => void = () => {};

  type Unit = { id: string; name: string; task_id: string; snapshot_id: string; status: string; adapter?: string };
  type Phase = { id: string; role?: string; name: string; status: string; attempt: { units: Unit[] } };
  type Run = { generation: number; phases: Phase[] };
  type Proposal = { id: string; title: string; prompt: string; depends_on: string[]; acceptance: string; agent?: string };
  type Delivery = { id: string; goal: string; status: string; reason: string; head: string; base_commit: string; run: Run; history: Run[]; agents?: string[]; selected_paths?: string[]; plan?: { tickets: Proposal[] }; summary?: string };
  type Change = { path: string; status: string; previous_path?: string };
  const labels: Record<string, string> = {pending:'待开始', queued:'排队中', running:'执行中', completed:'已完成', awaiting_review:'等待确认', accepted:'已通过', blocked:'已阻塞', awaiting_rework:'等待处理', failed:'执行失败', paused:'已暂停', cancelled:'已取消', recovery_required:'待核对'};
  const strategies = [
    {value:'auto',label:'由管家规划'}, {value:'fan_out',label:'并行分工'}, {value:'chain',label:'串行交接'},
    {value:'review',label:'代码审查'}, {value:'verify',label:'独立验证'}, {value:'discussion',label:'交叉讨论'}
  ];
  let list: Delivery[] = [];
  let selected = '', goal = initialGoal, feedback = '', strategy = inputSnapshot ? 'verify' : 'auto', error = '';
  let frozenInputs: string[] = inputSnapshot ? [inputSnapshot] : [];
  let chosenAgents: string[] = [];
  let pending = false, initialized = false;
  let retry: {fingerprint: string; key: string} | null = null;
  let files: Change[] = [], paths: string[] = [], changeVersion = '', filesReady = false;
  $: delivery = list.find(d => d.id === selected);
  $: canSelect = delivery?.status === 'completed' || (delivery?.status === 'awaiting_rework' && !!delivery.selected_paths?.length);
  $: verifiedPaths = delivery?.selected_paths?.length ? delivery.selected_paths : files.map(f => f.path);
  $: selectionMatches = paths.length === verifiedPaths.length && verifiedPaths.every(path => paths.includes(path));
  $: currentVersion = delivery && canSelect ? [delivery.id,delivery.run.generation,delivery.head].join('/') : '';
  $: if (currentVersion !== changeVersion) {
    changeVersion = currentVersion;
    filesReady = false;
    files = [];
    paths = [];
    if (delivery && currentVersion) void loadChanges(delivery.id, currentVersion);
  }

  async function loadChanges(id: string, version: string) {
    try {
      const result = await request<{files: Change[]}>('/deliveries/' + id + '/changes');
      if (version !== changeVersion) return;
      files = result.files;
      paths = delivery?.selected_paths?.length ? [...delivery.selected_paths] : files.map(f => f.path);
      filesReady = true;
    } catch (e) { if (version === changeVersion) error = errorText(e); }
  }

  let latestRead = 0;
  async function refresh() {
    const requestID = ++latestRead;
    try {
      const query = collaborationID ? '?collaboration_id=' + encodeURIComponent(collaborationID) : '';
      const data = await request<{deliveries: Delivery[]}>('/deliveries' + query);
      if (latestRead !== requestID) return;
      list = data.deliveries;
      if (!initialized && !inputSnapshot && !startNew) selected = list[0]?.id || '';
      initialized = true;
    } catch (e) { if (requestID === latestRead) error = errorText(e); }
  }
  onMount(() => {
    void refresh();
    const timer = setInterval(refresh, 1800);
    return () => { clearInterval(timer); latestRead++; changeVersion = ''; };
  });

  async function submit(path: string, body: object) {
    if (pending) return;
    const fingerprint = JSON.stringify([path, body]);
    if (retry?.fingerprint !== fingerprint) retry = {fingerprint, key: crypto.randomUUID()};
    pending = true;
    error = '';
    try {
      const result = await request<Delivery>(path, {method:'POST', headers:{'Idempotency-Key':retry.key}, body:JSON.stringify(body)});
      selected = result.id;
      initialized = true;
      retry = null;
      feedback = '';
      if (path === '/deliveries') goal = '';
      await refresh();
    } catch (e) { error = errorText(e); }
    finally { pending = false; }
  }
  function create() {
    if (!project || !goal.trim()) return;
    void submit('/deliveries', {orchestration:'smart', strategy, agents:chosenAgents, goal:goal.trim(), repository_path:project, context_ids:contextIDs, collaboration_id:collaborationID, ...(frozenInputs.length ? {input_snapshot_ids:frozenInputs} : {})});
  }
  function action(kind: string, phase?: Phase) {
    if (!delivery) return;
    void submit('/deliveries/' + delivery.id + '/actions', {kind, generation:delivery.run.generation, phase_id:phase?.id || '', snapshot_ids:phase?.attempt.units.map(u => u.snapshot_id) || [], feedback, ...(kind === 'select_paths' ? {paths} : {})});
  }
</script>

<div class="flow-panel">
  <div class="flow-toolbar">
    <Select ariaLabel="交付列表" bind:value={selected} options={[{value:'',label:'新交付'}, ...list.map(d => ({value:d.id,label:d.goal.slice(0,36) + ' · ' + (labels[d.status] || d.status)}))]}/>
    <button on:click={() => { initialized = true; selected = ''; goal = initialGoal; error = ''; }}>新建交付</button>
  </div>
  {#if error}<p role="alert" class="inline-error">{error}</p>{/if}
  {#if delivery}
    <p class="flow-goal">{delivery.goal}</p>
    <p class="flow-meta">第 {delivery.run.generation} 代 · {labels[delivery.status] || delivery.status}</p>
    {#if delivery.reason}<p class="flow-reason">{delivery.reason}</p>{/if}
    {#if delivery.plan?.tickets.length}
      <section class="flow-phase" aria-label="派工计划"><h3>派工计划</h3>
        {#each delivery.plan.tickets as ticket}
          <article><strong>{ticket.title}</strong><p>{ticket.prompt}</p>
            <dl><dt>验收条件</dt><dd>{ticket.acceptance}</dd><dt>执行引擎</dt><dd>{ticket.agent ? agentName(ticket.agent) : (delivery.agents || []).map(agentName).join('、') || '由管家分配'}</dd>
            {#if ticket.depends_on?.length}<dt>依赖</dt><dd>{ticket.depends_on.map(id => delivery?.plan?.tickets.find(t => t.id === id)?.title || id).join('、')}</dd>{/if}</dl>
          </article>
        {/each}
      </section>
    {/if}
    {#each delivery.run.phases as phase}
      <section class="flow-phase"><div class="flow-phase-title"><strong>{phase.name}</strong><span>{labels[phase.status] || phase.status}</span></div>
        {#each phase.attempt.units as unit}<div class="flow-unit">
          <button disabled={!unit.task_id} on:click={() => onTask(unit.task_id)}>{unit.name}</button><span>{unit.adapter ? agentName(unit.adapter) + ' · ' : ''}{labels[unit.status] || unit.status}</span>
          {#if unit.snapshot_id}<button on:click={() => onTask(unit.task_id,unit.snapshot_id)}>查看快照</button>{/if}
        </div>{/each}
        <div class="flow-actions">
          {#if phase.status === 'awaiting_review'}
            <button disabled={pending} on:click={() => action('approve',phase)}>{phase.role === 'planner' ? '确认此版计划并派工' : '确认此版本'}</button>
            <button disabled={pending} on:click={() => action('retry',phase)}>退回修改</button>
          {:else if ['blocked','awaiting_rework'].includes(delivery.status) && phase.status === 'running'}
            <button disabled={pending} on:click={() => action('retry',phase)}>重新执行</button>
          {/if}
          {#if phase.status === 'accepted' && delivery.status !== 'accepted'}<button disabled={pending} on:click={() => action('rollback',phase)}>回退到此阶段</button>{/if}
        </div>
      </section>
    {/each}
    {#if delivery.summary}<section class="flow-phase"><h3>交付摘要</h3><p class="summary">{delivery.summary}</p></section>{/if}
    {#if delivery.status === 'blocked'}<button class="project-picker" disabled={pending} on:click={() => action('resume')}>继续处理</button>{/if}
    {#if canSelect}
      <section class="flow-phase"><h3>接收到源项目</h3>
        {#if !filesReady}<p>正在读取候选版本…</p><button on:click={() => delivery && loadChanges(delivery.id,changeVersion)}>重新读取</button>{/if}
        {#each files as file}<label class="path"><input type="checkbox" bind:group={paths} value={file.path}/>{file.status} · {file.previous_path ? file.previous_path + ' → ' : ''}{file.path}</label>{/each}
        {#if filesReady && selectionMatches && delivery.status === 'completed'}
          <button class="project-picker" disabled={pending} on:click={() => delivery && submit('/deliveries/' + delivery.id + '/accept',{generation:delivery.run.generation})}>接收已验证版本</button>
        {:else if filesReady}
          <button class="project-picker" disabled={pending || !paths.length} on:click={() => action('select_paths')}>验证所选 {paths.length} 个文件</button>
        {/if}
      </section>
    {/if}
    <details><summary>返工反馈</summary><textarea aria-label="返工反馈" bind:value={feedback} rows="3"></textarea></details>
    {#if delivery.history.length}<details><summary>历史执行 · {delivery.history.length} 代</summary>{#each delivery.history as run}
      <details><summary>第 {run.generation} 代</summary>{#each run.phases as phase}{#each phase.attempt.units as u}{#if u.task_id}<button on:click={() => onTask(u.task_id)}>{phase.name} / {u.name}</button>{#if u.snapshot_id}<button on:click={() => onTask(u.task_id,u.snapshot_id)}>查看快照</button>{/if}{/if}{/each}{/each}</details>
    {/each}</details>{/if}
  {:else}
    <textarea aria-label="交付目标" rows="4" bind:value={goal} placeholder="描述要完成的目标…"></textarea>
    <div class="flow-create"><button class="project-picker" disabled={!!collaborationID} on:click={onSelectProject}>{folderName(project) || '选择项目文件夹'}</button>
      <Select ariaLabel="协作方式" bind:value={strategy} options={strategies}/>
    </div>
    {#if inputSnapshot}<p class="flow-meta">待验快照 · {inputSnapshot}</p>{/if}
    {#if snapshots.length}<details><summary>交接版本 · {frozenInputs.length ? '已选择 ' + frozenInputs.length + ' 份' : '各任务的最新版本'}</summary>
      {#each snapshots as v}<label class="path"><input type="checkbox" bind:group={frozenInputs} value={v.id}/>{v.task_title} · {new Date(v.created_at).toLocaleString()}</label>{/each}
    </details>{/if}
    <details><summary>候选引擎 · {chosenAgents.length ? chosenAgents.map(agentName).join('、') : '自动选择'}</summary>
      {#each agents as agent}<label class="path"><input type="checkbox" bind:group={chosenAgents} value={agent.id} disabled={!agent.available}/> {agent.name}{agent.available ? '' : ' · 不可用'}</label>{/each}
    </details>
    <button class="primary-button" disabled={pending || !project || !goal.trim()} on:click={create}>生成派工计划</button>
  {/if}
</div>

<style>
  .path{display:flex;align-items:center;gap:.5rem;overflow-wrap:anywhere;margin:.4rem 0}
  article + article{border-top:1px solid var(--border,#ddd);padding-top:.6rem}
  article p,.summary{white-space:pre-wrap;overflow-wrap:anywhere}
  dl{display:grid;grid-template-columns:max-content 1fr;gap:.25rem .8rem}dd{margin:0}
  .flow-phase h3{margin:0 0 .6rem}
</style>
