<template>
  <div class="dropdown inline-block">
    <button class="font-semibold py-1 rounded inline-flex items-center">
      <span class="mr-1">Project</span>
      <svg
        id="dropdown-caret"
        class="h-6 w-6 fill-current mr-2"
        viewBox="0 0 32 32"
        aria-hidden="true"
      >
        <path
          d="M16.003 18.626l7.081-7.081L25 13.46l-8.997 8.998-9.003-9 1.917-1.916z"
        />
      </svg>
    </button>
    <ul class="dropdown-menu absolute pt-1 w-full shadow-md">
      <li v-for="link in links" :key="link.url" class="text-center">
        <a
          :href="link.url"
          class="text-center rounded-t py-2 px-4 block whitespace-no-wrap"
          >{{ link.title }}</a
        >
      </li>
    </ul>
  </div>
</template>

<script>
export default {
  name: 'ProjectDropdown',

  data() {
    return {
      links: [
        { title: 'Roadmap', url: '/roadmap' },
        { title: 'Use Cases', url: '/usecases' },
        { title: 'FAQs', url: '/faqs' }
      ]
    }
  }
}
</script>
