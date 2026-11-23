<script lang="ts">
  import type { AgentInfo, EnginePolicy } from '$lib/blux/agents';
  export let info: AgentInfo | undefined;
  export let policy: EnginePolicy | undefined = undefined;
  $: effective = policy ?? info?.policy;
  const healthLabels:Record<string,string> = {healthy:'健康',degraded:'最近执行异常',unavailable:'不可用',unknown:'暂无执行记录'};
  const fields = [['sandbox','沙箱'],['approval','原生审批'],['network','网络'],['files','文件'],['tools','工具']] as const;
  const controls = [['sandbox','沙箱'],['approval','审批'],['network_control','网络控制'],['file_access','文件控制'],['tool_control','工具控制'],['usage_reporting','用量上报']] as const;
</script>
{#if effective || info}<details class="runtime-metadata">
  <summary>引擎能力与策略</summary>
  {#if info}<p>{info.name} · {info.available ? '可用' : '不可用'}{info.health ? ' · ' + (healthLabels[info.health.status] || info.health.status) : ''}</p>
    {#if info.health?.error_code}<p>{info.health.failure_class} · {info.health.error_code}{info.health.updated_at ? ' · ' + new Date(info.health.updated_at).toLocaleString() : ''}</p>{/if}
    <dl>{#each controls as [key,label]}<dt>{label}</dt><dd>{info.capabilities[key] ? '已接入' : '未接入'}</dd>{/each}</dl>
  {/if}
  {#if effective}<dl>{#each fields as [key,label]}{#if effective[key]}<dt>{label}策略</dt><dd>{effective[key]}</dd>{/if}{/each}<dt>策略依据</dt><dd>{effective.evidence}</dd></dl>{/if}
</details>{/if}
<style>dl{display:grid;grid-template-columns:max-content minmax(0,1fr);gap:.3rem .75rem}dd{margin:0;overflow-wrap:anywhere}</style>
