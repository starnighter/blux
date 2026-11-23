import type { Entry } from '$lib/components/blux/FileTree.svelte';

export type WorkbenchFile = {
	task: string;
	version: string;
	kind: 'artifact' | 'file' | 'diff';
	entry: Entry;
};

export type WorkbenchSnapshot = {
	id: string;
	turn_id: string;
	parent_snapshot_id?: string;
	commit: string;
	diff: string;
	artifacts: (Entry & {id: string; hash: string; size: number})[];
};

export function fileKey(file: WorkbenchFile) {
	return JSON.stringify([file.task, file.version, file.kind, file.entry.path]);
}

export type TextLine = {text: string; line?: number; side: 'old'|'new'};

export function linesWithLocation(text: string, diff: boolean): TextLine[] {
	let oldLine = 0;
	let newLine = 0;
	return text.split('\n').map((value, index) => {
		if (!diff) return {text:value, line:index+1, side:'new'};
		const header = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(value);
		if (header) {
			oldLine = Number(header[1]);
			newLine = Number(header[2]);
			return {text:value, side:'new'};
		}
		if (oldLine || newLine) {
			if (value.startsWith('-')) return {text:value, line:oldLine++, side:'old'};
			if (value.startsWith('+')) return {text:value, line:newLine++, side:'new'};
			if (value.startsWith(' ')) {
				oldLine++;
				return {text:value, line:newLine++, side:'new'};
			}
		}
		return {text:value, side:'new'};
	});
}
