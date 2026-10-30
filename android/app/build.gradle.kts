plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.plugin.compose")
}

// Release builds take their version from the tag (TWOCAN_VERSION=0.2.1 for
// v0.2.1). Android installs an update only over a lower versionCode, so it
// grows with each release: 2001 for 0.2.1.
val releaseVersion: String? = providers.environmentVariable("TWOCAN_VERSION").orNull

fun versionCodeOf(version: String): Int {
    val parts = version.split(".").map { it.toIntOrNull() }
    require(parts.size == 3 && parts.all { it != null && it in 0..999 }) {
        "TWOCAN_VERSION must look like 1.2.3, got \"$version\""
    }
    val (major, minor, patch) = parts.map { it!! }
    return major * 1_000_000 + minor * 1_000 + patch
}

// The release signing key: a PKCS12 file whose key, alias "twocan", has the
// file's password. Every release must be signed with it, or phones will not
// install it as an update (see the README).
val keystore: String? = providers.environmentVariable("TWOCAN_KEYSTORE").orNull

android {
    namespace = "app.twocan"
    compileSdk = 37
    ndkVersion = "29.0.14206865"

    defaultConfig {
        applicationId = "app.twocan"
        minSdk = 26
        targetSdk = 36
        versionCode = releaseVersion?.let(::versionCodeOf) ?: 1
        versionName = releaseVersion ?: "0.1.0"
    }

    signingConfigs {
        if (keystore != null) {
            create("release") {
                storeFile = file(keystore)
                storePassword = providers.environmentVariable("TWOCAN_KEYSTORE_PASSWORD").get()
                keyAlias = "twocan"
                keyPassword = storePassword
            }
        }
    }

    buildFeatures {
        compose = true
        buildConfig = true
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    buildTypes {
        release {
            // gomobile's generated classes are reached from Go through JNI,
            // so shrinking would remove them.
            isMinifyEnabled = false
            // Unsigned without TWOCAN_KEYSTORE.
            signingConfig = signingConfigs.findByName("release")
        }
    }
}

// Builds the Go core into libs/twocan.aar (see build-go.sh) whenever the Go
// code, or the helper addresses to bake in, changed.
val goAar = layout.projectDirectory.file("libs/twocan.aar")
val repoRoot: File = rootProject.projectDir.parentFile
val sdk = androidComponents.sdkComponents
val buildGo = tasks.register<Exec>("buildGo") {
    description = "Builds the Go core into libs/twocan.aar with gomobile."
    inputs.files(fileTree(repoRoot) {
        include("go.mod", "go.sum", "mobile/**/*.go", "internal/**/*.go")
        exclude("**/*_test.go")
    })
    inputs.property("bootstrap", providers.environmentVariable("TWOCAN_BOOTSTRAP").orElse(""))
    outputs.file(goAar)
    workingDir = repoRoot
    commandLine("sh", "android/build-go.sh")
    environment("ANDROID_HOME", sdk.sdkDirectory.get().asFile.path)
    environment("ANDROID_NDK_HOME", sdk.ndkDirectory.get().asFile.path)
}
tasks.named("preBuild") { dependsOn(buildGo) }

dependencies {
    implementation(files(goAar))

    implementation(platform("androidx.compose:compose-bom:2026.09.00"))
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.material:material-icons-core")
    implementation("androidx.activity:activity-compose:1.13.0")
    implementation("androidx.core:core-ktx:1.19.1")
    implementation("androidx.lifecycle:lifecycle-process:2.11.0")
    implementation("androidx.lifecycle:lifecycle-runtime-compose:2.11.0")
    implementation("androidx.lifecycle:lifecycle-viewmodel-compose:2.11.0")
    implementation("androidx.navigation:navigation-compose:2.10.2")
    implementation("com.journeyapps:zxing-android-embedded:4.3.0")

    testImplementation("junit:junit:4.13.2")
    // Android's org.json is a stub in local unit tests; this is the real one.
    testImplementation("org.json:json:20260814")
}
