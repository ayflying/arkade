import { onMounted, shallowRef, ref } from 'vue';
import { getAuthOptions } from '@/api/account';
import type { AuthOptions } from '@/types/settings';
export function useAuthOptions() {
    const options = shallowRef<AuthOptions | null>(null);
    const error = ref('');
    const loading = ref(false);
    async function load() {
        loading.value = true;
        error.value = '';
        try {
            options.value = await getAuthOptions();
        }
        catch (cause) {
            error.value = cause instanceof Error ? cause.message : '读取登录选项失败';
        }
        finally {
            loading.value = false;
        }
    }
    onMounted(load);
    return { options, error, loading, load };
}
