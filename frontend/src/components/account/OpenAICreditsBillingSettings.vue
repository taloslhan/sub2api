<template>
  <div class="border-t border-gray-200 pt-4 dark:border-dark-600">
    <div class="flex items-center justify-between gap-4">
      <div>
        <label class="input-label mb-0">使用 credits 价格计费</label>
        <p v-if="shadow" class="mt-1 text-xs text-gray-500 dark:text-gray-400">继承父账号的计费设置，请在父账号中修改。</p>
        <p v-else class="mt-1 text-xs text-gray-500 dark:text-gray-400">
          默认关闭，沿用现有计费。开启后，本账号的 Astra、Sol、Terra、Luna、Daybreak Blue 按订阅 credits 价格计费：Fast 为 2.5 倍，缓存写入免费，无长上下文附加费。用户费用与账号统计均使用此规则，分组和渠道自定义单价不再覆盖；业务倍率与 Free Fast 继续生效。
        </p>
      </div>
      <button v-if="!shadow" type="button" role="switch" data-testid="openai-credits-billing-toggle"
        aria-label="使用 credits 价格计费" :aria-checked="modelValue" @click="$emit('update:modelValue', !modelValue)"
        class="relative inline-flex h-6 w-11 flex-shrink-0 rounded-full border-2 border-transparent transition-colors focus:outline-none focus:ring-2 focus:ring-primary-500"
        :class="modelValue ? 'bg-primary-600' : 'bg-gray-200 dark:bg-dark-600'">
        <span class="pointer-events-none inline-block h-5 w-5 rounded-full bg-white shadow transition-transform"
          :class="modelValue ? 'translate-x-5' : 'translate-x-0'" />
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
defineProps<{ modelValue: boolean; shadow?: boolean }>()
defineEmits<{ 'update:modelValue': [value: boolean] }>()
</script>
