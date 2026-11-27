package com.shekh.bddm.data

fun extractUrl(text: String): String? {
    val r = Regex("""https?://[^\s<>"']+""")
    return r.find(text)?.value?.trimEnd('.', ',', ')', ']')
}
