<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Check, ChevronDown } from 'lucide-vue-next'
import { cn } from '~/utils/cn'

export interface SelectOption {
  value: string
  label: string
  disabled?: boolean
  group?: string
}

const props = withDefaults(
  defineProps<{
    modelValue?: string
    disabled?: boolean
    class?: string
    placeholder?: string
    id?: string
    /** Accessible name for the trigger button. */
    ariaLabel?: string
    /** Optional self-contained option list. */
    options?: SelectOption[]
  }>(),
  {
    modelValue: '',
    disabled: false,
    class: '',
    placeholder: 'Select…',
    id: undefined,
    ariaLabel: undefined,
    options: undefined,
  },
)

const emit = defineEmits<{
  (e: 'update:modelValue', value: string): void
  (e: 'change', value: string): void
}>()

const open = ref(false)
const triggerRef = ref<HTMLElement | null>(null)
const listRef = ref<HTMLElement | null>(null)
const activeIndex = ref(-1)
const slotLabel = ref('')

/**
 * The native <select> is never rendered. The browser's default dropdown ignores the
 * application theme entirely — it draws an OS-styled popup that clips, misaligns and
 * breaks the dark/light design language — so it is fully replaced by a custom listbox.
 *
 * Two rendering modes are supported:
 *  - `options` prop: the component owns the option list (self-contained dropdown).
 *  - default slot:  <option> children are read from the slot for backwards compatibility.
 */
const options = computed<SelectOption[]>(() => props.options ?? [])

const groupedOptions = computed<Record<string, SelectOption[]>>(() => {
  const groups: Record<string, SelectOption[]> = {}
  for (const opt of options.value) {
    const key = opt.group ?? ''
    if (!groups[key]) groups[key] = []
    groups[key].push(opt)
  }
  return groups
})

const displayLabel = computed(() => {
  const match = options.value.find((o) => o.value === props.modelValue)
  if (match) return match.label
  if (slotLabel.value) return slotLabel.value
  return props.placeholder
})

const hasValue = computed(() => options.value.some((o) => o.value === props.modelValue) || !!slotLabel.value)

function syncSlotLabel() {
  if (options.value.length) return
  // Fall back to reading the rendered <option> elements from the slot.
  const el = triggerRef.value?.closest('[data-select-root]')
  if (!el) return
  const optEl = el.querySelector('option[selected]') as HTMLOptionElement | null
  if (optEl) slotLabel.value = optEl.textContent?.trim() ?? ''
}

function openDropdown() {
  if (props.disabled || open.value) return
  open.value = true
  const idx = options.value.findIndex((o) => o.value === props.modelValue)
  activeIndex.value = idx >= 0 ? idx : 0
  nextTick(() => scrollActiveIntoView())
}

function closeDropdown() {
  open.value = false
  activeIndex.value = -1
}

function toggleDropdown() {
  if (open.value) closeDropdown()
  else openDropdown()
}

function selectOption(option: SelectOption) {
  if (option.disabled) return
  emit('update:modelValue', option.value)
  emit('change', option.value)
  closeDropdown()
  triggerRef.value?.focus()
}

function onKeydown(e: KeyboardEvent) {
  if (props.disabled) return
  const list = options.value.filter((o) => !o.disabled)

  switch (e.key) {
    case 'ArrowDown':
      e.preventDefault()
      if (!open.value) {
        openDropdown()
        return
      }
      activeIndex.value = Math.min(activeIndex.value + 1, list.length - 1)
      scrollActiveIntoView()
      break
    case 'ArrowUp':
      e.preventDefault()
      if (!open.value) {
        openDropdown()
        return
      }
      activeIndex.value = Math.max(activeIndex.value - 1, 0)
      scrollActiveIntoView()
      break
    case 'Home':
      if (open.value) {
        e.preventDefault()
        activeIndex.value = 0
        scrollActiveIntoView()
      }
      break
    case 'End':
      if (open.value) {
        e.preventDefault()
        activeIndex.value = list.length - 1
        scrollActiveIntoView()
      }
      break
    case 'Enter':
    case ' ':
      e.preventDefault()
      if (!open.value) {
        openDropdown()
        return
      }
      if (activeIndex.value >= 0 && list[activeIndex.value]) {
        selectOption(list[activeIndex.value])
      }
      break
    case 'Escape':
      if (open.value) {
        e.preventDefault()
        closeDropdown()
      }
      break
    case 'Tab':
      closeDropdown()
      break
  }
}

function scrollActiveIntoView() {
  nextTick(() => {
    const container = listRef.value
    if (!container) return
    const active = container.querySelector<HTMLElement>('[data-active="true"]')
    if (!active) return
    const cTop = container.scrollTop
    const cBottom = cTop + container.clientHeight
    const aTop = active.offsetTop
    const aBottom = aTop + active.offsetHeight
    if (aTop < cTop) container.scrollTop = aTop
    else if (aBottom > cBottom) container.scrollTop = aBottom - container.clientHeight
  })
}

function onDocumentPointerDown(e: MouseEvent) {
  if (!open.value) return
  const target = e.target as Node
  const root = triggerRef.value?.closest('[data-select-root]')
  if (root && root.contains(target)) return
  closeDropdown()
}

onMounted(() => {
  syncSlotLabel()
  document.addEventListener('pointerdown', onDocumentPointerDown)
})

onBeforeUnmount(() => {
  if (typeof document !== 'undefined') {
    document.removeEventListener('pointerdown', onDocumentPointerDown)
  }
})

watch(() => props.modelValue, syncSlotLabel)
watch(open, (isOpen) => {
  if (!isOpen) activeIndex.value = -1
})
</script>

<template>
  <div data-select-root class="relative" :class="props.class">
    <!-- Trigger -->
    <button
      :id="props.id"
      ref="triggerRef"
      type="button"
      role="combobox"
      :aria-label="props.ariaLabel"
      :aria-expanded="open"
      aria-haspopup="listbox"
      :disabled="props.disabled"
      :class="
        cn(
          'flex h-9 w-full items-center justify-between gap-2 rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm transition-colors',
          'hover:border-slate-300 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:border-primary',
          'disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:border-input',
          open && 'border-primary ring-2 ring-ring/30',
        )
      "
      @click="toggleDropdown"
      @keydown="onKeydown"
    >
      <span
        :class="
          cn(
            'truncate text-left',
            hasValue ? 'text-foreground' : 'text-muted-foreground',
          )
        "
      >
        {{ displayLabel }}
      </span>
      <ChevronDown
        class="h-4 w-4 shrink-0 text-muted-foreground transition-transform duration-150"
        :class="open ? 'rotate-180' : ''"
      />
    </button>

    <!-- Dropdown panel -->
    <Transition
      enter-active-class="transition duration-100 ease-out"
      enter-from-class="opacity-0 -translate-y-1 scale-[0.98]"
      enter-to-class="opacity-100 translate-y-0 scale-100"
      leave-active-class="transition duration-75 ease-in"
      leave-from-class="opacity-100 translate-y-0 scale-100"
      leave-to-class="opacity-0 -translate-y-1 scale-[0.98]"
    >
      <div
        v-if="open"
        ref="listRef"
        role="listbox"
        class="absolute left-0 right-0 z-50 mt-1 max-h-60 min-w-[8rem] overflow-y-auto overscroll-contain rounded-md border border-slate-200 bg-white p-1 shadow-lg shadow-slate-900/10 ring-1 ring-black/5"
      >
        <!-- Slot-based options (legacy usage) -->
        <slot />

        <!-- Prop-based options (self-contained usage) -->
        <template v-if="options.length">
          <template v-for="(group, groupName) in groupedOptions" :key="groupName">
            <div
              v-if="groupName"
              class="px-2 py-1.5 text-[11px] font-semibold uppercase tracking-wide text-muted-foreground"
            >
              {{ groupName }}
            </div>
            <button
              v-for="option in group"
              :id="`select-opt-${options.indexOf(option)}`"
              :key="option.value"
              type="button"
              role="option"
              :aria-selected="option.value === props.modelValue"
              :data-active="options.indexOf(option) === activeIndex"
              :disabled="option.disabled"
              :class="
                cn(
                  'flex w-full items-center justify-between gap-2 rounded-sm px-2 py-1.5 text-left text-sm transition-colors',
                  option.value === props.modelValue
                    ? 'bg-primary/10 font-medium text-primary'
                    : 'text-foreground hover:bg-muted',
                  option.disabled && 'cursor-not-allowed opacity-50 hover:bg-transparent',
                  options.indexOf(option) === activeIndex && option.value !== props.modelValue && 'bg-muted',
                )
              "
              @click="selectOption(option)"
              @mouseenter="activeIndex = options.indexOf(option)"
            >
              <span class="truncate">{{ option.label }}</span>
              <Check
                v-if="option.value === props.modelValue"
                class="h-3.5 w-3.5 shrink-0 text-primary"
              />
            </button>
          </template>
        </template>
      </div>
    </Transition>
  </div>
</template>
