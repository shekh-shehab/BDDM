package com.shekh.bddm.ui

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily

val PageDark = Color(0xFF14161A)
val SurfaceDark = Color(0xFF1F2228)
val NeutralDark = Color(0xFF2A2E36)
val TextDark = Color(0xFFEEF0F3)
val Text2Dark = Color(0xFFA3ABB8)
val MutedDark = Color(0xFF78808D)
val AccentDark = Color(0xFF5B8FF5)
val BgAccentDark = Color(0xFF22345C)
val TextAccentDark = Color(0xFF9BBAF9)
val SuccessDark = Color(0xFF3CC185)
val DangerDark = Color(0xFFEF7B6E)
val OrangeDark = Color(0xFFF0B47A)
val VioletDark = Color(0xFFB3A5F5)
val PrimaryBtn = Color(0xFFEEF0F3)
val OnPrimaryBtn = Color(0xFF14161A)

private val DarkColors = darkColorScheme(
    primary = AccentDark,
    onPrimary = Color.White,
    background = PageDark,
    onBackground = TextDark,
    surface = SurfaceDark,
    onSurface = TextDark,
    surfaceVariant = NeutralDark,
    onSurfaceVariant = Text2Dark,
    error = DangerDark,
    secondary = TextAccentDark,
)

private val LightColors = lightColorScheme(
    primary = Color(0xFF2F6FED),
    onPrimary = Color.White,
    background = Color(0xFFE6E9EE),
    onBackground = Color(0xFF1A1D21),
    surface = Color.White,
    onSurface = Color(0xFF1A1D21),
    surfaceVariant = Color(0xFFEEF0F3),
    onSurfaceVariant = Color(0xFF5B6370),
    error = Color(0xFFC0392B),
    secondary = Color(0xFF1F55C4),
)

@Composable
fun BDDMTheme(theme: String, content: @Composable () -> Unit) {
    val dark = when (theme) {
        "light" -> false
        "dark" -> true
        else -> isSystemInDarkTheme()
    }
    MaterialTheme(
        colorScheme = if (dark) DarkColors else LightColors,
        typography = MaterialTheme.typography,
        content = content,
    )
}
