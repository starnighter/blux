<script lang="ts">
  import { onMount } from 'svelte';
  import { request, errorText } from '$lib/blux/api';
  import { agentName, type AgentInfo, type EnginePolicy as Policy } from '$lib/blux/agents';
  import EnginePolicy from './EnginePolicy.svelte';
  export let taskID: string;
  export let agents: AgentInfo[] = [];
  export let onChange: () => void;
  type Attempt = { id:string; number:number; adapter:string; status:string; workspace_path:string; started_at:string; ended_at:string; error_code:string; error:string; failure_class:string; safe_to_retry:boolean; snapshot_id:string; turn_ids:string[]; policy?:Policy };
  type Ticket = {goal:string; dependencies:string[]; acceptance_criteria:string; status:string; attempts:Attempt[]};
  const labels: Record<string,string> = {queued:'排队中',running:'执行中',pausing:'正在暂停',paused:'已暂停',cancelling:'正在取消',cancelled:'已取消',completed:'已完成',failed:'执行失败',recovery_required:'待核对'};
  let ticket:Ticket | null = null;
  let error = '', adapter = '', pending = false, reading = 0;
  let retry: {body:string; key:string} | null = null;
  $: current = ticket?.attempts.at(-1);
  async function refresh() {
    const version = ++reading;
    try {
      const data = await request<Ticket>('/tasks/' + taskID + '/ticket');
      if (version === reading) ticket = data;
    } catch (e) { if (version === reading) error = errorText(e); }
  }
  onMount(() => {
    void refresh();
    const timer = setInterval(refresh, 2000);
    return () => { clearInterval(timer); reading++; };
  });
  async function act(action:string) {
    if (pending || !current) return;
    const body = JSON.stringify({action,expected_attempt_id:current.id,...(adapter && ['retry','resume'].includes(action) ? {adapter} : {})});
    if (retry?.body !== body) retry = {body,key:crypto.randomUUID()};
    pending = true;
    error = '';
    try {
      const result = await request<Ticket>('/tasks/' + taskID + '/ticket/actions',{method:'POST',headers:{'Idempotency-Key':retry.key},body});
      ++reading;
      ticket = result;
      adapter = '';
      retry = null;
      onChange();
    } catch (e) { error = errorText(e); }
    finally { pending = false; }
  }
</script>
<section class="ticket">
  <h3>工单{#if ticket} · {labels[ticket.status] || ticket.status}{/if}</h3>
  {#if ticket}
    <p>{ticket.goal}</p>
    {#if ticket.acceptance_criteria}<p>验收条件 · {ticket.acceptance_criteria}</p>{/if}
    {#if ticket.dependencies?.length}<p>依赖 · {ticket.dependencies.join('、')}</p>{/if}
    <div class="actions">
      {#if ['queued','running'].includes(ticket.status)}
        <button disabled={pending} on:click={() => act('pause')}>暂停</button>
      {/if}
      {#if ['queued','running','paused'].includes(ticket.status)}
        <button disabled={pending} on:click={() => act('cancel')}>取消工单</button>
      {/if}
      {#if ['paused','failed','recovery_required'].includes(ticket.status)}
        {#if ticket.status === 'recovery_required'}<p>请先核对工作区已有改动。确认重试将从当前文件继续执行。</p>{/if}
        <label>执行引擎 <select bind:value={adapter}><option value="">沿用当前引擎</option>{#each agents as a}<option value={a.id} disabled={!a.available}>{a.name}</option>{/each}</select></label>
        <button disabled={pending} on:click={() => act(ticket?.status === 'paused' ? 'resume' : 'retry')}>{ticket.status === 'paused' ? '恢复' : ticket.status === 'recovery_required' ? '已核对，重试' : '重试'}</button>
      {/if}
    </div>
    {#each [...ticket.attempts].reverse() as attempt}<details class="runtime-metadata">
      <summary>第 {attempt.number} 次 · {agentName(attempt.adapter)} · {labels[attempt.status] || attempt.status}</summary>
      <dl>{#if attempt.started_at}<dt>开始</dt><dd>{new Date(attempt.started_at).toLocaleString()}</dd>{/if}
        {#if attempt.ended_at}<dt>结束</dt><dd>{new Date(attempt.ended_at).toLocaleString()}</dd>{/if}
        {#if attempt.workspace_path}<dt>工作区</dt><dd>{attempt.workspace_path}</dd>{/if}
        {#if attempt.snapshot_id}<dt>结果快照</dt><dd>{attempt.snapshot_id}</dd>{/if}
        {#if attempt.error_code}<dt>失败原因</dt><dd>{attempt.failure_class} · {attempt.error_code}<br/>{attempt.error}</dd>{/if}
      </dl>
      {#if attempt.policy}<EnginePolicy info={agents.find(a => a.id === attempt.adapter)} policy={attempt.policy}/>{:else}<p>此执行尚无策略记录。</p>{/if}
    </details>{/each}
  {/if}
  {#if error}<p class="inline-error" role="alert">{error}</p>{/if}
</section>
<style>
  .ticket p,.ticket dd{overflow-wrap:anywhere}.ticket h3{font-size:1rem}.actions{display:flex;flex-wrap:wrap;gap:.5rem;align-items:center;margin:.5rem 0}
  button,select{border:1px solid var(--border,#ccc);border-radius:.4rem;padding:.25rem .55rem;color:inherit;background:transparent}
  dl{display:grid;grid-template-columns:max-content minmax(0,1fr);gap:.3rem .6rem}dd{margin:0}
</style>
