import com.google.protobuf.gradle.proto

plugins {
    `java-library`
    alias(libs.plugins.protobuf)
}

description = "Generated protobuf-lite classes of the PandaEye wire protocol"

dependencies {
    api(libs.protobuf.javalite)
}

sourceSets {
    main {
        proto {
            srcDir(rootProject.layout.projectDirectory.dir("../proto"))
            // stream.proto describes cloud-internal bus payloads; plugins never see them.
            exclude("pandaeye/v1/stream.proto")
        }
    }
}

protobuf {
    protoc {
        artifact = libs.protoc.get().toString()
    }
    generateProtoTasks {
        all().configureEach {
            builtins {
                named("java") { option("lite") }
            }
        }
    }
}

tasks.withType<JavaCompile>().configureEach {
    options.compilerArgs.add("-Xlint:none")
}
