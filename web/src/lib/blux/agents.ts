import type { AgentAdapter } from './types';
export const agentNames: Record<AgentAdapter, string> = {
 codex: 'Codex', claude: 'Claude Code', deepseek: 'DeepSeek Harness', gemini: 'Gemini CLI'
};
export const agentName = (id: string) => agentNames[id as AgentAdapter] || id;
export interface EnginePolicy {sandbox:string;approval:string;network:string;files:string;tools:string;evidence:string}
export interface AgentInfo { id: AgentAdapter; name: string; available: boolean; version: string; reason?: string; health?:{status:string;failure_class?:string;error_code?:string;updated_at?:string};policy?:EnginePolicy; capabilities: { sandbox?:boolean;approval?:boolean;network_control?:boolean;file_access?:boolean;tool_control?:boolean;usage_reporting?:boolean; streaming: boolean; resume: boolean; steer: boolean; interrupt: boolean } }
export function agentOptions(agents: AgentInfo[] = [], filter = false) {
 return (Object.entries(agentNames) as [AgentAdapter,string][]).map(([value,label]) => {
  const info = agents.find(agent => agent.id === value);
  return { value, label, agent: value, disabled: !filter && info?.available === false, description: !filter && info?.available === false ? (info.reason?.includes('未安装')||info.reason?.includes('找不到')?'未安装':'不可用') : undefined, reason: info?.reason };
 });
}
