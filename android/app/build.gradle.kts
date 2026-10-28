plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.plugin.compose")
}

android {
    namespace = "app.twocan"
    compileSdk = 37
    ndkVersion = "29.0.14206865"

    defaultConfig {
        applicationId = "app.twocan"
        minSdk = 26
        targetSdk = 36
        versionCode = 1
        versionName = "0.1.0"
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
