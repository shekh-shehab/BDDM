package com.shekh.bddm.data

import android.content.Context
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.Canvas
import android.webkit.WebView
import java.io.File
import java.io.FileOutputStream

object TabThumbs {
    private fun root(ctx: Context) = File(ctx.cacheDir, "tabthumbs").apply { mkdirs() }
    fun thumbFile(ctx: Context, tabId: String) = File(root(ctx), "$tabId.jpg")
    fun favFile(ctx: Context, tabId: String) = File(root(ctx), "$tabId-fav.png")

    fun capture(view: WebView): Bitmap? {
        val w = view.width
        val h = view.height
        if (w <= 0 || h <= 0) return null
        return try {
            val full = Bitmap.createBitmap(w, h, Bitmap.Config.RGB_565)
            val canvas = Canvas(full)
            canvas.drawColor(0xFF14161A.toInt())
            view.draw(canvas)
            val tw = 420
            val th = (tw.toFloat() / w * h).toInt().coerceAtLeast(1)
            Bitmap.createScaledBitmap(full, tw, th, true).also { if (it !== full) full.recycle() }
        } catch (_: Throwable) { null }
    }

    fun saveThumb(ctx: Context, tabId: String, bmp: Bitmap) {
        try {
            FileOutputStream(thumbFile(ctx, tabId)).use { bmp.compress(Bitmap.CompressFormat.JPEG, 70, it) }
        } catch (_: Exception) {}
    }

    fun saveFavicon(ctx: Context, tabId: String, bmp: Bitmap) {
        try {
            FileOutputStream(favFile(ctx, tabId)).use { bmp.compress(Bitmap.CompressFormat.PNG, 100, it) }
        } catch (_: Exception) {}
    }

    fun loadThumb(ctx: Context, tabId: String): Bitmap? {
        val f = thumbFile(ctx, tabId)
        return if (f.isFile) BitmapFactory.decodeFile(f.absolutePath) else null
    }

    fun loadFavicon(ctx: Context, tabId: String): Bitmap? {
        val f = favFile(ctx, tabId)
        return if (f.isFile) BitmapFactory.decodeFile(f.absolutePath) else null
    }

    fun delete(ctx: Context, tabId: String) {
        thumbFile(ctx, tabId).delete()
        favFile(ctx, tabId).delete()
    }
}
