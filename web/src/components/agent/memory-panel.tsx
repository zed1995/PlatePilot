// memory-panel.tsx is where "system remembered something about you" becomes
// inspectable.
//
// The agent writes memories as a side effect of conversation, and a side effect
// the user cannot read is a side effect the user cannot correct. Listing them,
// editing them and deleting them is the whole feature — the memory.saved event
// only says it happened.
import { useEffect, useState } from 'react'
import { Loader2, Pencil, Trash2 } from 'lucide-react'

import type { MemoryView } from '../../api/types'
import { formatTime } from '../../format'
import { useDeleteMemory, useMemories, useUpdateMemory } from '../../hooks/useMemories'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { Textarea } from '../ui/textarea'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '../ui/dialog'
import { EmptyState } from '../empty-state'
import { ErrorState } from '../error-state'
import { StatusTag } from '../status-tag'

export interface MemoryPanelProps {
  // refreshToken is bumped by the page when a memory.saved event arrives, so the
  // list picks up a row the stream just created without a manual reload. The
  // event carries the content but not the id-to-row shape the list needs.
  refreshToken?: number
}

export function MemoryPanel({ refreshToken = 0 }: MemoryPanelProps) {
  const memories = useMemories()
  const update = useUpdateMemory()
  const remove = useDeleteMemory()
  const [editing, setEditing] = useState<string | null>(null)
  const [pendingDelete, setPendingDelete] = useState<MemoryView | null>(null)

  useEffect(() => {
    if (refreshToken > 0) void memories.refetch()
    // memories is intentionally not a dependency: a new refetch identity on
    // every render would loop. The token is the trigger.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [refreshToken])

  if (memories.error) {
    return (
      <ErrorState
        title="记忆读取失败"
        description={
          memories.error instanceof Error ? memories.error.message : String(memories.error)
        }
        onRetry={() => void memories.refetch()}
      />
    )
  }

  const rows = memories.data ?? []

  return (
    <div className="space-y-2" data-testid="memory-panel">
      <p className="m-0 text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">
        长期记忆（{rows.length}）
      </p>

      {memories.isLoading && rows.length === 0 ? (
        <p className="m-0 text-[12px] text-ink-tertiary">读取中…</p>
      ) : rows.length === 0 ? (
        <EmptyState
          title="还没有记忆"
          description="说一句「记住我不吃辣」之类的话，Agent 写下的记忆会出现在这里。"
        />
      ) : (
        <ul className="m-0 list-none space-y-1.5 p-0">
          {rows.map((memory) => (
            <li
              key={memory.id}
              data-testid={`memory-${memory.id}`}
              className="rounded-lg border border-[var(--border-subtle)] bg-surface-solid px-2.5 py-2"
            >
              {editing === memory.id ? (
                <MemoryEditor
                  memory={memory}
                  saving={update.isPending}
                  onCancel={() => setEditing(null)}
                  onSave={(patch) =>
                    update.mutate(
                      { memoryId: memory.id, ...patch },
                      { onSuccess: () => setEditing(null) },
                    )
                  }
                />
              ) : (
                <>
                  <div className="flex items-center gap-2">
                    <StatusTag tone="grey">{memory.memory_type}</StatusTag>
                    <span className="ml-auto text-[11px] text-ink-tertiary">
                      {formatTime(memory.updated_at)}
                    </span>
                  </div>
                  <p className="m-0 mt-1 text-[12px] leading-[1.6] text-ink">{memory.content}</p>
                  <div className="mt-1.5 flex items-center gap-1.5">
                    {memory.source && (
                      <span className="text-[11px] text-ink-tertiary">
                        source={memory.source} · confidence={memory.confidence}
                      </span>
                    )}
                    <Button
                      variant="ghost"
                      size="sm"
                      className="ml-auto"
                      onClick={() => setEditing(memory.id)}
                    >
                      <Pencil className="h-3 w-3" />
                      改
                    </Button>
                    <Button variant="ghost" size="sm" onClick={() => setPendingDelete(memory)}>
                      <Trash2 className="h-3 w-3" />
                      删
                    </Button>
                  </div>
                </>
              )}
            </li>
          ))}
        </ul>
      )}

      {/* Deleting a memory is not undoable from here, so it goes through a
          dialog rather than a second click on the same button. */}
      <Dialog open={pendingDelete !== null} onOpenChange={(open) => !open && setPendingDelete(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>删除这条记忆？</DialogTitle>
            <DialogDescription>
              「{pendingDelete?.content}」删除后不可恢复，Agent 之后也不会再引用它。
            </DialogDescription>
          </DialogHeader>
          <div className="flex justify-end gap-2">
            <Button variant="outline" size="sm" onClick={() => setPendingDelete(null)}>
              取消
            </Button>
            <Button
              variant="destructive"
              size="sm"
              disabled={remove.isPending}
              onClick={() => {
                if (!pendingDelete) return
                remove.mutate(pendingDelete.id, { onSuccess: () => setPendingDelete(null) })
              }}
            >
              {remove.isPending && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
              删除
            </Button>
          </div>
        </DialogContent>
      </Dialog>

      {update.error && (
        <p className="m-0 text-[11px] text-error">
          修改失败：{update.error instanceof Error ? update.error.message : String(update.error)}
        </p>
      )}
      {remove.error && (
        <p className="m-0 text-[11px] text-error">
          删除失败：{remove.error instanceof Error ? remove.error.message : String(remove.error)}
        </p>
      )}
    </div>
  )
}

// MemoryEditor keeps the draft local and sends only what changed.
//
// PATCH is "at least one field", so an untouched field must be absent rather
// than sent back identical — the server would take it as a new write and stamp a
// new updated_at on a memory nobody edited.
export function MemoryEditor({
  memory,
  saving,
  onSave,
  onCancel,
}: {
  memory: MemoryView
  saving: boolean
  onSave: (patch: { content?: string; memory_type?: string }) => void
  onCancel: () => void
}) {
  const [content, setContent] = useState(memory.content)
  const [memoryType, setMemoryType] = useState(memory.memory_type)
  const contentChanged = content.trim() !== memory.content
  const typeChanged = memoryType.trim() !== memory.memory_type
  const canSave = !saving && (contentChanged || typeChanged) && content.trim() !== '' && memoryType.trim() !== ''

  return (
    <div className="space-y-2">
      <Input
        value={memoryType}
        aria-label="memory_type"
        onChange={(event) => setMemoryType(event.target.value)}
        className="h-8"
      />
      <Textarea
        value={content}
        aria-label="记忆内容"
        onChange={(event) => setContent(event.target.value)}
        className="min-h-[64px]"
      />
      <div className="flex items-center justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={onCancel} disabled={saving}>
          取消
        </Button>
        <Button
          size="sm"
          disabled={!canSave}
          onClick={() =>
            onSave({
              ...(contentChanged ? { content: content.trim() } : {}),
              ...(typeChanged ? { memory_type: memoryType.trim() } : {}),
            })
          }
        >
          {saving && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
          保存
        </Button>
      </div>
    </div>
  )
}
