import renderMapping from './render-mapping.json';

/**
 * 渲染提示 - 指导前端如何显示每种事件类型
 */
export interface RenderHint {
  component: string;
  heading?: string;
  show_header?: boolean;
  collapse?: boolean;
  stream_mode?: boolean;
  merge_consecutive?: boolean;
  max_preview_lines?: number;
}

/**
 * 根据事件类型获取渲染提示
 */
export function getRenderHint(eventType: string): RenderHint | undefined {
  const entry = renderMapping.events.find((e: any) => e.event === eventType);
  if (!entry) return undefined;
  
  const hint: RenderHint = {
    component: entry.component || 'DefaultCard',
  };
  if (entry.heading) hint.heading = entry.heading;
  if (entry.show_header) hint.show_header = true;
  if (entry.collapse) hint.collapse = true;
  if (entry.stream_mode) hint.stream_mode = true;
  if (entry.merge_consecutive) hint.merge_consecutive = true;
  if (entry.max_preview_lines) hint.max_preview_lines = entry.max_preview_lines;
  
  return hint;
}

/**
 * 已知的事件类型列表（由协议定义）
 */
export const KNOWN_EVENT_TYPES = renderMapping.events.map((e: any) => e.event);

/**
 * 渲染组件映射 - 可用于动态组件选择
 */
export const COMPONENT_MAP: Record<string, string> = {};
for (const entry of renderMapping.events) {
  if (entry.component) {
    COMPONENT_MAP[entry.event] = entry.component;
  }
}
